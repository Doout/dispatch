package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/oklog/ulid/v2"
)

// ProvisionService executes a template without creating a workflow revision or
// job result: those records otherwise retain plaintext job outputs.
func (s *Service) ProvisionService(ctx context.Context, resource core.WorkflowResource, projectID string, inputs map[string]string, runs ...core.ServiceProvisionRun) (map[string]string, error) {
	if !resource.Active || resource.Kind != KindServiceTemplate {
		return nil, errors.New("service template is unavailable")
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].ServiceTemplate == nil {
		return nil, errors.New("stored service template is invalid")
	}
	spec := documents[0].ServiceTemplate
	if spec.Provision.Neon != nil {
		return nil, errors.New("Neon provisioning requires durable owned-service acceptance")
	}
	if spec.Provision.Docker != nil || spec.Provision.Helm != nil {
		run := core.ServiceProvisionRun{ID: ulid.Make().String(), TemplateID: resource.ID, ProjectID: projectID, ServiceName: resource.Name}
		if len(runs) > 0 {
			run = runs[0]
		}
		request := core.ServiceProvisionRequest{Run: run, ServiceType: spec.ServiceType, Inputs: inputs, Outputs: spec.Provision.Outputs, ConfigSHA: resource.ConfigSHA}
		if d := spec.Provision.Docker; d != nil {
			server, err := s.Store.GetServer(ctx, d.ServerRef)
			if err != nil {
				return nil, errors.New("Docker provisioner server is unavailable")
			}
			if server.AgentNodeID != "" {
				if s.RemoteDockerServices == nil {
					return nil, errors.New("remote service provisioning is unavailable")
				}
				return s.RemoteDockerServices(ctx, request, *d, server)
			}
			return (deploy.DockerExecutor{}).Provision(ctx, request, *d, server)
		}
		h := *spec.Provision.Helm
		if run.Target != nil {
			h.Namespace = run.Target.Namespace
		}
		server, err := s.Store.GetServer(ctx, h.ServerRef)
		if err != nil {
			return nil, errors.New("Helm provisioner server is unavailable")
		}
		return (deploy.HelmExecutor{}).Provision(ctx, request, h, server)
	}
	var source core.ConfigSource
	if len(spec.Sources) > 0 {
		source, err = s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
		if err != nil || source.ProjectID != projectID {
			return nil, errors.New("repository access for the template is unavailable")
		}
	}
	snapshot := map[string]core.WorkflowSourceRevision{}
	for alias, ref := range spec.Sources {
		sha, err := s.repositoryHead(ctx, source, ref.Repository, sourceRevisionRef(ref))
		if err != nil {
			return nil, fmt.Errorf("resolve source %s: %w", alias, err)
		}
		snapshot[alias] = core.WorkflowSourceRevision{Alias: alias, Repository: ref.Repository, Branch: sourceRevisionRef(ref), CommitSHA: sha, Path: ref.Path}
	}
	root, err := os.MkdirTemp("", "dispatch-service-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	revision := core.WorkflowRevision{ID: ulid.Make().String(), ResourceID: resource.ID, Sources: snapshot, CreatedAt: time.Now().UTC()}
	runtime := &jobRuntime{service: s, serviceTemplate: &resource, source: source, revision: revision, root: root, paths: map[string]string{}, inputs: map[string]string{}}
	defer runtime.close()
	secrets, err := runtime.resolveSecrets(ctx, spec.Provision.Secrets)
	if err != nil {
		return nil, err
	}
	for name, value := range inputs {
		key := "DISPATCH_INPUT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		if _, exists := secrets[key]; exists {
			return nil, fmt.Errorf("input %s conflicts with a configured environment variable", name)
		}
		secrets[key] = value
	}
	outputs, _, err := runtime.runJobCommand(ctx, spec.Provision.JobSpec, secrets, func(string) {})
	if err != nil {
		return nil, errors.New("provisioner failed; check the provider before retrying")
	}
	return outputs, nil
}

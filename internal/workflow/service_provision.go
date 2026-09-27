package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

// ProvisionService executes a template without creating a workflow revision or
// job result: those records otherwise retain plaintext job outputs.
func (s *Service) ProvisionService(ctx context.Context, resource core.WorkflowResource, inputs map[string]string) (map[string]string, error) {
	if !resource.Active || resource.Kind != KindServiceTemplate {
		return nil, errors.New("service template is unavailable")
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].ServiceTemplate == nil {
		return nil, errors.New("stored service template is invalid")
	}
	spec := documents[0].ServiceTemplate
	source, err := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return nil, err
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
	runtime := &jobRuntime{service: s, source: source, revision: revision, root: root, paths: map[string]string{}, inputs: map[string]string{}}
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
	outputs, _, err := runtime.runJobCommand(ctx, spec.Provision, secrets, func(string) {})
	if err != nil {
		return nil, errors.New("provisioner failed; check the provider before retrying")
	}
	return outputs, nil
}

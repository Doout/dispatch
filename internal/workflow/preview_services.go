package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type PreviewServiceSpec struct {
	TemplateRef   string `json:"templateRef" yaml:"templateRef"`
	CleanupPolicy string `json:"cleanupPolicy,omitempty" yaml:"cleanupPolicy,omitempty"`
}

const previewServicePrefix = "preview:"

func PreviewServiceAlias(ref string) (string, bool) {
	return strings.TrimPrefix(ref, previewServicePrefix), strings.HasPrefix(ref, previewServicePrefix)
}

// PreviewServiceTemplate resolves metadata only; provider credentials remain in
// the controller's owned-service execution path after source trust succeeds.
func (s *Service) PreviewServiceTemplate(ctx context.Context, project, ref string) (core.WorkflowResource, ServiceTemplateSpec, error) {
	var resource core.WorkflowResource
	saved, err := s.Store.GetSavedServiceTemplate(ctx, ref)
	if err == nil {
		if saved.ProjectID != project {
			return resource, ServiceTemplateSpec{}, errors.New("preview service template belongs to another project")
		}
		resource = core.WorkflowResource{ID: saved.ID, Name: saved.Name, Kind: KindServiceTemplate, Path: "template.yaml", Document: saved.Document, ConfigSHA: saved.Digest, SpecDigest: saved.Digest, Active: true}
	} else if errors.Is(err, store.ErrNotFound) {
		resource, err = s.Store.GetWorkflowResource(ctx, ref)
		if err != nil {
			return resource, ServiceTemplateSpec{}, err
		}
		source, e := s.Store.GetConfigSource(ctx, resource.ConfigSourceID)
		if e != nil || source.ProjectID != project {
			return resource, ServiceTemplateSpec{}, errors.New("preview service template belongs to another project")
		}
	} else {
		return resource, ServiceTemplateSpec{}, err
	}
	docs, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || !resource.Active || len(docs) != 1 || docs[0].ServiceTemplate == nil || docs[0].ServiceTemplate.Provision.Neon == nil {
		return resource, ServiceTemplateSpec{}, errors.New("preview service requires an active Neon ServiceTemplate")
	}
	spec := *docs[0].ServiceTemplate
	if spec.Provision.Neon.DataMode == "parent-data" {
		return resource, spec, errors.New("automatic preview databases require schema-only or an already masked parent template")
	}
	providers, ok := s.Store.(store.NeonStore)
	if !ok {
		return resource, spec, errors.New("preview service storage unavailable")
	}
	provider, err := providers.GetNeonProvider(ctx, spec.Provision.Neon.ProviderRef)
	if err != nil || provider.ProjectID != project {
		return resource, spec, errors.New("Neon provider is unavailable in this project")
	}
	return resource, spec, nil
}
func (s *Service) previewServiceScopes(ctx context.Context, project string, spec ApplicationSpec) ([]string, error) {
	scopes := []string{}
	for alias, p := range spec.PreviewServices {
		resource, definition, err := s.PreviewServiceTemplate(ctx, project, p.TemplateRef)
		if err != nil {
			return nil, err
		}
		scopes = append(scopes, "preview-service:"+alias+":"+resource.ID+":"+resource.ConfigSHA+":"+definition.Provision.Neon.ProviderRef)
	}
	return scopes, nil
}
func validatePreviewServiceReferences(spec ApplicationSpec) error {
	for alias, p := range spec.PreviewServices {
		if !aliasPattern.MatchString(alias) || p.TemplateRef == "" || p.CleanupPolicy != "" && p.CleanupPolicy != "retain" {
			return errors.New("previewServices require named templateRef entries with retain cleanup policy")
		}
	}
	check := func(ref string) error {
		if alias, ok := PreviewServiceAlias(ref); ok {
			if _, exists := spec.PreviewServices[alias]; !exists {
				return fmt.Errorf("preview service %s is not declared", alias)
			}
		}
		return nil
	}
	for _, d := range spec.Deployments {
		for _, b := range d.ServiceBindings {
			if err := check(b.ServiceRef); err != nil {
				return err
			}
		}
	}
	for _, stage := range spec.Stages {
		for _, ref := range stage.ServiceBindings {
			if err := check(ref); err != nil {
				return err
			}
		}
	}
	for _, jobs := range []map[string]JobSpec{spec.Jobs, spec.Finally} {
		for _, job := range jobs {
			for _, b := range job.Secrets {
				if b.ServiceRef != "" {
					if _, ok := PreviewServiceAlias(b.ServiceRef); !ok {
						return errors.New("job serviceRef must name a declared preview: service")
					}
					if err := check(b.ServiceRef); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func (s *Service) previewServiceID(ctx context.Context, resourceID, ref string) (string, error) {
	alias, ok := PreviewServiceAlias(ref)
	if !ok {
		return ref, nil
	}
	data, ok := s.Store.(store.NeonStore)
	if !ok {
		return "", errors.New("preview service storage unavailable")
	}
	run, err := data.GetNeonPreviewService(ctx, resourceID, alias)
	if err != nil {
		return "", errors.New("preview database is not ready")
	}
	resource, err := s.Store.(store.ServiceResourceStore).GetServiceResource(ctx, run)
	if err != nil || resource.State != "ready" {
		return "", errors.New("preview database is not ready")
	}
	return resource.ServiceID, nil
}
func (s *Service) resolvePreviewDeploymentBindings(ctx context.Context, resourceID string, spec DeploymentSpec, stage StageSpec) (DeploymentSpec, StageSpec, error) {
	bindings := append([]core.ServiceBinding(nil), spec.ServiceBindings...)
	for i, b := range bindings {
		ref := b.ServiceRef
		if v, ok := stage.ServiceBindings[b.Alias]; ok {
			ref = v
		}
		id, err := s.previewServiceID(ctx, resourceID, ref)
		if err != nil {
			return spec, stage, err
		}
		bindings[i].ServiceRef = id
	}
	spec.ServiceBindings = bindings
	stage.ServiceBindings = nil
	return spec, stage, nil
}

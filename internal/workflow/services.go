package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
)

func validateServiceDestinations(spec DeploymentSpec) error {
	dummy := map[string]core.Service{}
	for _, b := range spec.ServiceBindings {
		if b.ServiceRef == "" {
			return fmt.Errorf("service binding %s requires serviceRef", b.Alias)
		}
		fields := map[string]core.ServiceField{}
		if b.Helm != nil {
			for _, field := range b.Helm.Keys {
				fields[field] = core.ServiceField{Configured: true}
			}
			paths := append([]string{}, b.Helm.SecretNameValues...)
			for p := range b.Helm.KeyValues {
				paths = append(paths, p)
			}
			for _, p := range paths {
				for other := range spec.Helm.Bindings {
					if p == other || strings.HasPrefix(p, other+".") || strings.HasPrefix(other, p+".") {
						return fmt.Errorf("service binding destination %s conflicts with an output binding", p)
					}
				}
			}
		}
		old := dummy[b.ServiceRef]
		if old.Fields == nil {
			old.Fields = map[string]core.ServiceField{}
		}
		for k, v := range fields {
			old.Fields[k] = v
		}
		dummy[b.ServiceRef] = old
	}
	return serviceconn.ValidateBindings(spec.ServiceBindings, core.BuildTypeHelm, dummy)
}
func (s *Service) effectiveServiceBindings(ctx context.Context, project string, spec DeploymentSpec, stage StageSpec) ([]core.ServiceBinding, error) {
	if err := validateServiceDestinations(spec); err != nil {
		return nil, err
	}
	if len(spec.ServiceBindings) == 0 {
		return []core.ServiceBinding{}, nil
	}
	services, err := s.Store.ListServices(ctx, project)
	if err != nil {
		return nil, err
	}
	byRef := map[string]core.Service{}
	for _, item := range services {
		byRef[item.Name] = item
		byRef[item.ID] = item
	}
	result := append([]core.ServiceBinding{}, spec.ServiceBindings...)
	for i, b := range result {
		if ref, ok := stage.ServiceBindings[b.Alias]; ok {
			b.ServiceRef = ref
		}
		item, ok := byRef[b.ServiceRef]
		if !ok {
			return nil, fmt.Errorf("service binding %s references an unavailable service in this project", b.Alias)
		}
		b.ServiceRef = item.ID
		result[i] = b
	}
	if err := serviceconn.ValidateBindings(result, core.BuildTypeHelm, byRef); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *Service) validateApplicationServices(ctx context.Context, project string, doc Document) error {
	if doc.Spec == nil {
		return nil
	}
	for _, spec := range doc.Spec.Deployments {
		if _, err := s.effectiveServiceBindings(ctx, project, spec, StageSpec{}); err != nil {
			return err
		}
	}
	for _, stage := range doc.Spec.Stages {
		aliases := map[string]bool{}
		for _, name := range stage.Deploy {
			spec := doc.Spec.Deployments[name]
			for _, b := range spec.ServiceBindings {
				aliases[b.Alias] = true
			}
			if _, err := s.effectiveServiceBindings(ctx, project, spec, stage); err != nil {
				return fmt.Errorf("stage %s: %w", stage.Name, err)
			}
		}
		for alias, ref := range stage.ServiceBindings {
			if !aliases[alias] || ref == "" {
				return fmt.Errorf("stage %s overrides unknown service alias %s or has an empty reference", stage.Name, alias)
			}
		}
	}
	return nil
}
func (s *Service) ServiceReferenced(ctx context.Context, project, id, name string) (bool, error) {
	sources, err := s.Store.ListConfigSources(ctx)
	if err != nil {
		return false, err
	}
	allowed := map[string]bool{}
	for _, source := range sources {
		if source.ProjectID == project {
			allowed[source.ID] = true
		}
	}
	resources, err := s.Store.ListWorkflowResources(ctx, "")
	if err != nil {
		return false, err
	}
	for _, r := range resources {
		if !allowed[r.ConfigSourceID] || r.State == "removed" {
			continue
		}
		docs, err := Parse(r.Path, []byte(r.Document))
		if err != nil {
			return false, err
		}
		for _, doc := range docs {
			if doc.Spec == nil {
				continue
			}
			for _, spec := range doc.Spec.Deployments {
				for _, b := range spec.ServiceBindings {
					if b.ServiceRef == id || b.ServiceRef == name {
						return true, nil
					}
				}
			}
			for _, stage := range doc.Spec.Stages {
				for _, ref := range stage.ServiceBindings {
					if ref == id || ref == name {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}

func (s *Service) applicationServiceIDs(ctx context.Context, project string, doc Document) ([]string, error) {
	seen := map[string]bool{}
	ids := []string{}
	add := func(bindings []core.ServiceBinding) {
		for _, b := range bindings {
			if !seen[b.ServiceRef] {
				seen[b.ServiceRef] = true
				ids = append(ids, b.ServiceRef)
			}
		}
	}
	if doc.Spec == nil {
		return ids, nil
	}
	for _, spec := range doc.Spec.Deployments {
		b, err := s.effectiveServiceBindings(ctx, project, spec, StageSpec{})
		if err != nil {
			return nil, err
		}
		add(b)
	}
	for _, stage := range doc.Spec.Stages {
		for _, name := range stage.Deploy {
			b, err := s.effectiveServiceBindings(ctx, project, doc.Spec.Deployments[name], stage)
			if err != nil {
				return nil, err
			}
			add(b)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

package api

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/workflow"
	"gopkg.in/yaml.v3"
)

type workflowPreviewLinkChange struct {
	Number int
	Remove bool
}

func parseWorkflowPreviewLinks(arguments string, sources map[string]workflow.SourceSpec, originRepository string, originNumber int) (map[string]workflowPreviewLinkChange, error) {
	result := map[string]workflowPreviewLinkChange{}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return result, nil
	}
	action, rest, found := strings.Cut(arguments, " ")
	action = strings.ToLower(action)
	if !found || action != "with" && action != "without" {
		return nil, errors.New("use 'with alias=#123' to link a PR or 'without alias' to unlink it")
	}
	remove := action == "without"
	parts := strings.FieldsFunc(strings.TrimSpace(rest), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(parts) == 0 {
		return nil, fmt.Errorf("provide a source after '%s'", action)
	}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimPrefix(strings.TrimSpace(value), "#")
		number := 0
		if ok {
			var err error
			number, err = strconv.Atoi(value)
			if err != nil || number < 1 {
				return nil, fmt.Errorf("invalid linked pull request %q", part)
			}
		}
		if key == "" || !remove && !ok {
			return nil, fmt.Errorf("invalid linked pull request %q", part)
		}
		alias := ""
		for name, source := range sources {
			repository := events.NormalizeRepository(source.Repository)
			_, short, _ := strings.Cut(repository, "/")
			if strings.EqualFold(name, key) || repository == key || short == key {
				if alias != "" {
					return nil, fmt.Errorf("component %q is ambiguous; use an alias or full repository", key)
				}
				alias = name
			}
		}
		if alias == "" {
			return nil, fmt.Errorf("unknown component %q", key)
		}
		if _, found := result[alias]; found {
			return nil, fmt.Errorf("component %q was provided more than once", alias)
		}
		if events.NormalizeRepository(sources[alias].Repository) == events.NormalizeRepository(originRepository) {
			if remove {
				return nil, fmt.Errorf("component %q is the primary PR and cannot be unlinked", alias)
			}
			if number != originNumber {
				return nil, fmt.Errorf("component %q conflicts with the command pull request", alias)
			}
			continue
		}
		result[alias] = workflowPreviewLinkChange{Number: number, Remove: remove}
	}
	return result, nil
}

func workflowPreviewDefaults(document string) (map[string]core.WorkflowPreviewSourceDefault, error) {
	// Templates have already been validated. Decode their configured sources
	// without instantiating a second Application or changing instance metadata.
	var value workflow.ApplicationDocument
	if err := yaml.Unmarshal([]byte(document), &value); err != nil {
		return nil, err
	}
	defaults := map[string]core.WorkflowPreviewSourceDefault{}
	for alias, source := range value.Spec.Sources {
		if source.Ref == "" && source.Branch == "" {
			source.Branch = "main"
		}
		defaults[alias] = core.WorkflowPreviewSourceDefault{Repository: source.Repository, Branch: source.Branch, Ref: source.Ref}
	}
	return defaults, nil
}

func (a *API) prepareWorkflowPreviewLinks(ctx context.Context, resource core.WorkflowResource, trigger core.WorkflowPreviewTrigger, changes map[string]workflowPreviewLinkChange) (core.WorkflowResource, core.WorkflowPreviewTrigger, error) {
	documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return resource, trigger, errors.New("temporary preview document is invalid")
	}
	document := documents[0]
	links := map[string]int{}
	for alias, number := range trigger.LinkedPullRequests {
		links[alias] = number
	}
	defaults := map[string]core.WorkflowPreviewSourceDefault{}
	for alias, source := range trigger.SourceDefaults {
		defaults[alias] = source
	}
	for alias, change := range changes {
		source := document.Spec.Sources[alias]
		linkedNumber := links[alias]
		if change.Remove && linkedNumber == 0 {
			continue // Repeating an unlink command is safe.
		}
		if change.Remove && change.Number != 0 && change.Number != linkedNumber {
			return resource, trigger, fmt.Errorf("%s is linked to PR #%d, not #%d", alias, linkedNumber, change.Number)
		}
		if _, saved := defaults[alias]; linkedNumber == 0 && (!saved || source.Branch != "") {
			defaults[alias] = core.WorkflowPreviewSourceDefault{Repository: source.Repository, Branch: source.Branch, Ref: source.Ref}
		}
		if _, ok := defaults[alias]; !ok && trigger.TemplateID != "" {
			template, err := a.store.GetWorkflowPreviewTemplate(ctx, trigger.TemplateID)
			if err != nil {
				return resource, trigger, fmt.Errorf("load source defaults: %w", err)
			}
			recovered, err := workflowPreviewDefaults(template.Document)
			if err != nil {
				return resource, trigger, fmt.Errorf("read source defaults: %w", err)
			}
			if recoveredSource, ok := recovered[alias]; ok {
				defaults[alias] = recoveredSource
			}
		}
		if change.Remove {
			original, ok := defaults[alias]
			// A branch explicitly restored in the editor takes precedence over
			// the saved default. Never guess a branch from a pinned PR commit.
			if source.Branch != "" {
				original = core.WorkflowPreviewSourceDefault{Repository: source.Repository, Branch: source.Branch}
				defaults[alias], ok = original, true
			}
			if !ok || events.NormalizeRepository(original.Repository) != events.NormalizeRepository(source.Repository) ||
				strings.Contains(original.Ref+original.Branch+original.Repository, "{{") {
				return resource, trigger, fmt.Errorf("cannot restore %s's configured source; set its default branch in Edit YAML & PR before unlinking", alias)
			}
			source.Ref, source.Branch = original.Ref, original.Branch
			document.Spec.Sources[alias] = source
			delete(links, alias)
		} else {
			links[alias] = change.Number
		}
	}
	encoded, err := document.MarshalYAML()
	if err != nil {
		return resource, trigger, err
	}
	resource.Document = string(encoded)
	trigger.LinkedPullRequests, trigger.SourceDefaults = links, defaults
	return resource, trigger, nil
}

func pinWorkflowPreviewSources(resource core.WorkflowResource, refs map[string]string) (core.WorkflowResource, error) {
	documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return resource, errors.New("temporary preview document is invalid")
	}
	document := documents[0]
	for alias, ref := range refs {
		source, ok := document.Spec.Sources[alias]
		if !ok || ref == "" {
			return resource, fmt.Errorf("preview source %q is invalid", alias)
		}
		source.Ref, source.Branch = ref, ""
		document.Spec.Sources[alias] = source
	}
	digest, err := document.Digest()
	if err != nil {
		return resource, err
	}
	encoded, err := document.MarshalYAML()
	if err != nil {
		return resource, err
	}
	resource.Document, resource.SpecDigest, resource.UpdatedAt = string(encoded), digest, time.Now().UTC()
	return resource, nil
}

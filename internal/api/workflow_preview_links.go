package api

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/workflow"
)

// A temporary workflow uses the same command form as a preview group:
// /preview with ui=#123 or /preview with owner/repository=#123.
func parseWorkflowPreviewLinks(arguments string, sources map[string]workflow.SourceSpec, originRepository string, originNumber int) (map[string]int, error) {
	result := map[string]int{}
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return result, nil
	}
	if !strings.HasPrefix(strings.ToLower(arguments), "with ") {
		return nil, errors.New("use 'with alias=#123' to attach another pull request")
	}
	parts := strings.FieldsFunc(strings.TrimSpace(arguments[5:]), func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(parts) == 0 {
		return nil, errors.New("provide a pull request after 'with'")
	}
	for _, part := range parts {
		key, value, ok := strings.Cut(part, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimPrefix(strings.TrimSpace(value), "#")
		number, err := strconv.Atoi(value)
		if !ok || key == "" || err != nil || number < 1 {
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
			if number != originNumber {
				return nil, fmt.Errorf("component %q conflicts with the command pull request", alias)
			}
			continue
		}
		result[alias] = number
	}
	return result, nil
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

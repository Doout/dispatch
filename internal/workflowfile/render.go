package workflowfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

func SelectJSONKey(contents []byte, path string) (string, error) {
	if !jsonKeyPathPattern.MatchString(path) {
		return "", errors.New("invalid JSON key path")
	}
	var object map[string]any
	if err := json.Unmarshal(contents, &object); err != nil || object == nil {
		return "", errors.New("value is not a JSON object")
	}
	var current any = object
	for _, key := range strings.Split(path, ".") {
		fields, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("JSON key %s is not available", path)
		}
		current, ok = fields[key]
		if !ok {
			return "", fmt.Errorf("JSON key %s is not available", path)
		}
	}
	switch value := current.(type) {
	case string:
		return value, nil
	case bool, float64:
		return fmt.Sprint(value), nil
	default:
		return "", fmt.Errorf("JSON key %s must contain a string, number, or boolean", path)
	}
}

func RenderRuntime(value string, paths map[string]string, sources map[string]core.WorkflowSourceRevision, inputs map[string]string, stage *StageSpec) (string, error) {
	var renderErr error
	rendered := templatePattern.ReplaceAllStringFunc(value, func(expression string) string {
		parts := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(expression, "{{"), "}}")), ".")
		switch {
		case len(parts) == 3 && parts[0] == "sources":
			source, ok := sources[parts[1]]
			if !ok {
				renderErr = fmt.Errorf("source %s is unavailable", parts[1])
				return ""
			}
			switch parts[2] {
			case "path":
				return paths[parts[1]]
			case "commit":
				return source.CommitSHA
			case "branch":
				return source.Branch
			}
		case len(parts) == 2 && parts[0] == "inputs":
			value, ok := inputs[parts[1]]
			if !ok {
				renderErr = fmt.Errorf("input %s is unavailable", parts[1])
				return ""
			}
			return value
		case len(parts) == 2 && parts[0] == "stage" && stage != nil:
			if parts[1] == "name" {
				return stage.Name
			}
			if parts[1] == "url" {
				return stage.URL
			}
		}
		renderErr = fmt.Errorf("template %s is unavailable", expression)
		return ""
	})
	return rendered, renderErr
}

func RenderValues(input map[string]any, sources map[string]core.WorkflowSourceRevision, stage StageSpec) (map[string]any, error) {
	result := map[string]any{}
	for key, value := range input {
		rendered, err := RenderValue(value, sources, stage)
		if err != nil {
			return nil, err
		}
		result[key] = rendered
	}
	return result, nil
}

func RenderValue(value any, sources map[string]core.WorkflowSourceRevision, stage StageSpec) (any, error) {
	switch current := value.(type) {
	case string:
		return RenderRuntime(current, nil, sources, nil, &stage)
	case map[string]any:
		return RenderValues(current, sources, stage)
	case []any:
		result := make([]any, len(current))
		for index, item := range current {
			rendered, err := RenderValue(item, sources, stage)
			if err != nil {
				return nil, err
			}
			result[index] = rendered
		}
		return result, nil
	default:
		return value, nil
	}
}

func SetNestedValue(values map[string]any, path string, value any) error {
	parts := strings.Split(path, ".")
	current := values
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("Helm value path %s is invalid", path)
		}
		if index == len(parts)-1 {
			current[part] = value
			return nil
		}
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	return nil
}

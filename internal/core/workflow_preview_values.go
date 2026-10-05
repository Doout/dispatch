package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// WorkflowPreviewValuesCommand carries literal Helm values from a PR comment.
// Deployment is empty for an unqualified override or clear of all overrides.
type WorkflowPreviewValuesCommand struct {
	Deployment string
	Clear      bool
	Values     map[string]any
}

// DecodeWorkflowPreviewValues preserves integer Helm values through JSON
// persistence without leaving json.Number values for YAML to encode as strings.
func DecodeWorkflowPreviewValues(raw []byte) (map[string]map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var values map[string]map[string]any
	if err := decoder.Decode(&values); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("preview values must contain one JSON object")
	}
	for deployment, overlay := range values {
		value, err := normalizePreviewNumbers(overlay)
		if err != nil {
			return nil, err
		}
		values[deployment] = value.(map[string]any)
	}
	return values, nil
}

func CloneWorkflowPreviewValues(values map[string]map[string]any) (map[string]map[string]any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return DecodeWorkflowPreviewValues(raw)
}

func normalizePreviewNumbers(value any) (any, error) {
	switch current := value.(type) {
	case json.Number:
		if strings.ContainsAny(string(current), ".eE") {
			return current.Float64()
		}
		if signed, err := current.Int64(); err == nil {
			return signed, nil
		}
		return strconv.ParseUint(string(current), 10, 64)
	case map[string]any:
		for key, item := range current {
			normalized, err := normalizePreviewNumbers(item)
			if err != nil {
				return nil, err
			}
			current[key] = normalized
		}
	case []any:
		for index, item := range current {
			normalized, err := normalizePreviewNumbers(item)
			if err != nil {
				return nil, err
			}
			current[index] = normalized
		}
	}
	return value, nil
}

package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/doout/dispatch/internal/core"
)

func clonePreviewValues(values map[string]map[string]any) (map[string]map[string]any, error) {
	return core.CloneWorkflowPreviewValues(values)
}

func samePreviewValues(a, b map[string]map[string]any) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func (s *Service) capturePreviewValues(ctx context.Context, resource core.WorkflowResource) (map[string]map[string]any, error) {
	if !resource.Temporary {
		return nil, nil
	}
	triggers, err := s.Store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, err
	}
	var selected *core.WorkflowPreviewTrigger
	for index := range triggers {
		if triggers[index].ResourceID == resource.ID && triggers[index].ClosedAt == nil {
			if selected != nil {
				return nil, errors.New("preview has multiple active value settings")
			}
			selected = &triggers[index]
		}
	}
	if selected == nil || len(selected.PreviewValues) == 0 {
		return nil, nil
	}
	documents, err := Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return nil, errors.New("preview configuration is invalid")
	}
	for deployment := range selected.PreviewValues {
		if _, exists := documents[0].Spec.Deployments[deployment]; !exists {
			return nil, fmt.Errorf("preview values refer to missing deployment %q; clear or replace its values before running", deployment)
		}
	}
	return clonePreviewValues(selected.PreviewValues)
}

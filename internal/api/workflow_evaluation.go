package api

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// Enrich only resources that have already passed the caller's project filter.
func (a *API) enrichWorkflowEvaluations(ctx context.Context, resources []core.WorkflowResource) error {
	reader, ok := a.store.(interface {
		GetWorkflowEquivalence(context.Context, string) (core.WorkflowEquivalence, error)
	})
	if !ok {
		return nil
	}
	for index := range resources {
		if resources[index].Kind != "Application" || !resources[index].Active || resources[index].State == "removed" || resources[index].State == "invalid" {
			continue
		}
		observation, err := reader.GetWorkflowEquivalence(ctx, resources[index].ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		resources[index].LastEvaluation = &observation
	}
	return nil
}

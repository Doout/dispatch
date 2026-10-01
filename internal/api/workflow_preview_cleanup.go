package api

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/deploy"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

// A generated preview is one workflow, even when it has several Helm releases.
func (a *API) removeWorkflowPreviewForApp(ctx context.Context, id string) (bool, error) {
	return a.removeWorkflowPreviewForAppReviewed(ctx, id, nil)
}

func (a *API) removeWorkflowPreviewForAppReviewed(ctx context.Context, id string, review func() error) (bool, error) {
	app, err := a.store.GetApp(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	resourceID := app.HelmProvenance.WorkflowResourceID
	if !app.Generated || resourceID == "" {
		return false, nil
	}
	resource, err := a.store.GetWorkflowResource(ctx, resourceID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !resource.Temporary {
		return false, nil
	}
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	if review != nil {
		if err := review(); err != nil {
			return true, err
		}
	}
	if _, err := a.workflows.Deactivate(ctx, resource.ID); err != nil {
		return true, err
	}
	if err := a.workflows.CancelPreviewRuns(ctx, resource.ID); err != nil {
		return true, err
	}
	if err := a.cleanupWorkflowPreviewResource(ctx, resource); err != nil {
		return true, err
	}
	return true, a.store.RemoveWorkflowPreviewResource(ctx, resource.ID, time.Now().UTC())
}

// Older cleanup paths paused resources after closing all their PR triggers.
// Retry their cleanup, then archive them. Paused or expired open previews remain.
func (a *API) reconcileRemovedWorkflowPreviews(ctx context.Context) error {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	resources, err := a.store.ListWorkflowResources(ctx, "")
	if err != nil {
		return err
	}
	closed := map[string]bool{}
	open := map[string]bool{}
	for _, t := range triggers {
		if t.ClosedAt == nil {
			open[t.ResourceID] = true
		} else {
			closed[t.ResourceID] = true
		}
	}
	var joined error
	for _, r := range resources {
		if !r.Temporary || r.State == "removed" || !closed[r.ID] || open[r.ID] {
			continue
		}
		if err := a.workflows.CancelPreviewRuns(ctx, r.ID); err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		if err := a.cleanupWorkflowPreviewResource(ctx, r); err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		joined = errors.Join(joined, a.store.RemoveWorkflowPreviewResource(ctx, r.ID, time.Now().UTC()))
	}
	return joined
}

func previewRemoved(resource core.WorkflowResource) bool { return resource.State == "removed" }

func (a *API) previewCleanupProblem(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errDestructiveReviewChanged):
		problem(w, http.StatusConflict, "Resource changed", err.Error())
	case errors.Is(err, deploy.ErrDeploymentActive):
		problem(w, http.StatusConflict, "Deployment stopping", "The preview is paused. Wait for its deployment to stop, then retry cleanup.")
	case errors.Is(err, deploy.ErrCleanupUnsupported):
		problem(w, http.StatusConflict, "Cleanup unavailable", err.Error())
	default:
		a.notFoundOrInternal(w, err, "Preview")
	}
}

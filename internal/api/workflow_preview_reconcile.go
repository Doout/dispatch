package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func (a *API) beginWorkflowPreviewCleanup(ctx context.Context, resource core.WorkflowResource, reason string, now time.Time) (core.WorkflowPreviewCleanup, error) {
	apps, err := a.workflowPreviewCleanupApps(ctx, resource)
	if err != nil {
		return core.WorkflowPreviewCleanup{}, err
	}
	for _, app := range apps {
		if err = a.store.RegisterLegacyPreviewApp(ctx, resource.ID, app.ID); err != nil {
			return core.WorkflowPreviewCleanup{}, err
		}
	}
	return a.store.BeginWorkflowPreviewCleanup(ctx, resource.ID, reason, now)
}

func (a *API) reconcileWorkflowPreviewCleanup(ctx context.Context, id string) error {
	cleanup, err := a.store.ClaimWorkflowPreviewCleanup(ctx, id, time.Now().UTC())
	if err != nil || cleanup == nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cleanupErr := a.workflows.CancelPreviewRuns(work, cleanup.ResourceID)
	if cleanupErr == nil {
		for _, entry := range cleanup.Apps {
			var unresolved error
			if runtime, ok := a.store.(store.RuntimeJobStore); ok {
				active, e := runtime.ActiveRuntimeMutation(work, entry.AppID)
				if e != nil {
					unresolved = e
				} else if active != nil && (active.ID != entry.JobID || active.State == "unknown") {
					unresolved = errors.New("earlier runtime operation is unresolved; inspect and acknowledge its outcome before cleanup")
				}
			}
			if entry.State == "succeeded" && unresolved == nil {
				continue
			}
			app, err := a.store.GetApp(work, entry.AppID)
			if err == nil {
				err = unresolved
			}
			if err == nil && (app.ServerID != entry.ServerID || app.SpecDigest() != entry.SpecDigest) {
				err = errors.New("owned preview application changed after cleanup acceptance")
			}
			if err == nil && entry.NodeID != "" {
				server, e := a.store.GetServer(work, entry.ServerID)
				if e != nil {
					err = e
				} else if server.AgentNodeID != entry.NodeID {
					err = errors.New("cleanup target agent changed; inspect its retained ownership before recovery")
				}
				if err == nil {
					data, ok := a.store.(store.RuntimeJobStore)
					if !ok {
						return errors.New("runtime evidence unavailable")
					}
					credential, e := data.GetEdgeCredential(work, entry.NodeID)
					if e != nil {
						err = e
					} else if credential.Generation != entry.NodeGeneration || credential.Revoked {
						err = errors.New("cleanup target enrollment changed; inspect its retained ownership before recovery")
					}
				}
			}
			if err == nil {
				entry, err = a.store.RetryWorkflowPreviewCleanupApp(work, *cleanup, entry)
			}
			if err == nil {
				err = a.deploy.CancelAndWaitOwnedApplication(work, app.ID)
			}
			if err == nil && app.State != "closed" {
				err = a.deploy.CleanupOwnedApplication(work, app, entry.JobID)
			}
			entry.State, entry.Error = "succeeded", ""
			if err != nil {
				entry.State, entry.Error = "blocked", err.Error()
				cleanupErr = errors.Join(cleanupErr, fmt.Errorf("clean preview app %s: %w", entry.AppID, err))
			}
			// A cancelled request still leaves inspectable progress and resumable intent.
			saveCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			saveErr := a.store.SaveWorkflowPreviewCleanupApp(saveCtx, *cleanup, entry)
			done()
			if saveErr != nil {
				cleanupErr = errors.Join(cleanupErr, saveErr)
				break
			}
		}
	}
	detail := ""
	if cleanupErr != nil {
		detail = "Preview cleanup will retry: " + cleanupErr.Error()
	}
	saveCtx, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer done()
	return errors.Join(cleanupErr, a.store.FinishWorkflowPreviewCleanup(saveCtx, *cleanup, detail, time.Now().UTC()))
}

func (a *API) resumeWorkflowPreviewCleanups(ctx context.Context) error {
	cleanups, err := a.store.ListWorkflowPreviewCleanups(ctx, "")
	if err != nil {
		return err
	}
	var joined error
	for _, cleanup := range cleanups {
		if ctx.Err() != nil {
			return errors.Join(joined, ctx.Err())
		}
		joined = errors.Join(joined, a.reconcileWorkflowPreviewCleanup(ctx, cleanup.ID))
	}
	return joined
}

func previewCleanupAlreadyEnded(err error) bool { return errors.Is(err, store.ErrPreviewClosing) }

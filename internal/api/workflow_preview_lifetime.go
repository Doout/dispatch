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
)

func renewWorkflowPreviewLifetime(trigger *core.WorkflowPreviewTrigger, now time.Time) error {
	duration, err := workflow.ParsePreviewTTL(trigger.TTL)
	if err != nil {
		return err
	}
	trigger.TTL = workflow.NormalizePreviewTTL(trigger.TTL)
	trigger.ExpiresAt = nil
	if duration > 0 {
		deadline := now.Add(duration)
		trigger.ExpiresAt = &deadline
	}
	return nil
}

func workflowPreviewLifetimeText(trigger core.WorkflowPreviewTrigger) string {
	if trigger.ExpiresAt == nil {
		return "No time limit."
	}
	return "Scheduled shutdown: " + trigger.ExpiresAt.UTC().Format(time.RFC3339) + "."
}

// Cleanup has its own clock so slow GitHub polling cannot delay shutdown.
func (a *API) RunPreviewExpirer(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := errors.Join(a.expireWorkflowPreviews(ctx, time.Now().UTC()), a.reconcileRemovedWorkflowPreviews(ctx)); err != nil && a.logger != nil {
			a.logger.Error("preview expiry cleanup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) expireWorkflowPreviews(ctx context.Context, now time.Time) error {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, trigger := range triggers {
		if trigger.ClosedAt != nil || trigger.ExpiresAt == nil || trigger.ExpiresAt.After(now) {
			continue
		}
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		_, err = a.beginWorkflowPreviewCleanup(ctx, resource, "expired", now)
		if err != nil && !previewCleanupAlreadyEnded(err) {
			joined = errors.Join(joined, err)
		}
	}
	return errors.Join(joined, a.resumeWorkflowPreviewCleanups(ctx))
}

func (a *API) processWorkflowPreviewLifetimeComment(ctx context.Context, target *previewPollTarget, event core.IncomingEvent) error {
	fields := strings.Fields(event.Arguments)
	// Read fresh state for webhook/poll overlap and successive comments in one scan.
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	for _, previous := range triggers {
		if previous.ClosedAt != nil || previous.GitHubAppID != target.connectionID || events.NormalizeRepository(previous.Repository) != target.repository || previous.PullRequestNumber != event.PullRequestNumber || previous.Command != event.Command {
			continue
		}
		resource, err := a.store.GetWorkflowResource(ctx, previous.ResourceID)
		if err != nil {
			return err
		}
		handled, err := a.store.WorkflowPreviewLifetimeCommentHandled(ctx, previous.ID, event.SourceCommentID)
		if err != nil {
			return err
		}
		if handled {
			return a.workflowPreviewActivity(ctx, target, event, previous, resource, "", "processed", workflowPreviewLifetimeText(previous))
		}
		latestID, err := a.store.LatestWorkflowPreviewDeployComment(ctx, previous.ID)
		if err != nil {
			return err
		}
		id, _ := strconv.ParseUint(event.SourceCommentID, 10, 64)
		latest, _ := strconv.ParseUint(latestID, 10, 64)
		if id > 0 && latest > id {
			return a.workflowPreviewActivity(ctx, target, event, previous, resource, "", "superseded", "A newer preview comment replaced this command.")
		}
		if len(fields) != 2 {
			return fmt.Errorf("use %s ttl 1d, %s ttl 0, or %s extend 1d", event.Command, event.Command, event.Command)
		}
		duration, err := workflow.ParsePreviewTTL(fields[1])
		if err != nil {
			return err
		}
		if fields[0] == "extend" && duration == 0 {
			return errors.New("provide a positive extension; use ttl 0 to remove the time limit")
		}
		now := time.Now().UTC()
		next := previous
		if fields[0] == "ttl" {
			next.TTL = workflow.NormalizePreviewTTL(fields[1])
			if err := renewWorkflowPreviewLifetime(&next, now); err != nil {
				return err
			}
		} else {
			if previous.ExpiresAt == nil {
				return errors.New("this preview has no time limit; use ttl 1d to set one")
			}
			deadline := previous.ExpiresAt.Add(duration)
			next.ExpiresAt = &deadline
		}
		_, err = a.store.SaveWorkflowPreviewLifetime(ctx, previous, next, event.SourceCommentID, now)
		if err != nil {
			if !resource.Active {
				return fmt.Errorf("preview is %s; post %s to deploy it again before changing its lifetime", resource.State, previous.Command)
			}
			return err
		}
		// Refresh the existing deployment report rather than adding a comment for
		// every extension. The receipt survives a failed GitHub update.
		// Reporting retries independently so a GitHub outage does not hold the poll cursor.

		return a.workflowPreviewActivity(ctx, target, event, next, resource, "", "processed", workflowPreviewLifetimeText(next))
	}
	return fmt.Errorf("no preview exists for this PR; post %s first", event.Command)
}

func (a *API) reportPendingWorkflowPreviewLifetimes(ctx context.Context) error {
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, trigger := range triggers {
		if !trigger.LifetimeReportPending {
			continue
		}
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		joined = errors.Join(joined, a.store.MarkWorkflowPreviewLifetimeReported(ctx, trigger, resource.State))
	}
	return joined
}

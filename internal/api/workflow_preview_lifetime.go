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
		if err := a.expireWorkflowPreviews(ctx, time.Now().UTC()); err != nil && a.logger != nil {
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
		leaseUntil := time.Now().UTC().Add(10 * time.Minute)
		claimed, err := a.store.ClaimWorkflowPreviewExpiry(ctx, trigger.ID, now, leaseUntil)
		if err != nil || !claimed {
			joined = errors.Join(joined, err)
			continue
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		resource, cleanupErr := a.store.GetWorkflowResource(cleanupCtx, trigger.ResourceID)
		if cleanupErr == nil {
			cleanupErr = a.workflows.CancelPreviewRuns(cleanupCtx, resource.ID)
		}
		if cleanupErr == nil {
			cleanupErr = a.cleanupWorkflowPreviewResource(cleanupCtx, resource)
		}
		cancel()
		detail := ""
		if cleanupErr != nil {
			detail = "Preview lifetime ended; cleanup will retry: " + cleanupErr.Error()
			joined = errors.Join(joined, fmt.Errorf("expire preview %s: %w", trigger.ResourceID, cleanupErr))
		}
		joined = errors.Join(joined, a.store.FinishWorkflowPreviewExpiry(ctx, trigger.ID, leaseUntil, detail, time.Now().UTC()))
	}
	return joined
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
		if err := a.refreshWorkflowPreviewLifetimeReport(ctx, previous.ID); err != nil && a.logger != nil {
			a.logger.Warn("preview lifetime report pending", "error", err)
		}
		return a.workflowPreviewActivity(ctx, target, event, next, resource, "", "processed", workflowPreviewLifetimeText(next))
	}
	return fmt.Errorf("no preview exists for this PR; post %s first", event.Command)
}

func (a *API) refreshWorkflowPreviewLifetimeReport(ctx context.Context, triggerID string) error {
	if a.eventConfig.GitHubApps == nil {
		return nil
	}
	a.previewReportMu.Lock()
	defer a.previewReportMu.Unlock()
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	for _, trigger := range triggers {
		if trigger.ID != triggerID {
			continue
		}
		if trigger.ReportCommentID == "" {
			resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
			if err != nil {
				return err
			}
			return a.store.MarkWorkflowPreviewLifetimeReported(ctx, trigger, resource.State)
		}
		revisions, err := a.store.ListWorkflowRevisions(ctx, trigger.ResourceID, 0)
		if err != nil {
			return err
		}
		for _, revision := range revisions {
			if revision.State != "succeeded" || strings.HasPrefix(revision.Trigger, "pull request test ") {
				continue
			}
			resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
			if err != nil {
				return err
			}
			stages, err := a.store.ListWorkflowStageRuns(ctx, revision.ID)
			if err != nil {
				return err
			}
			connection, err := a.store.GetGitHubApp(ctx, trigger.GitHubAppID)
			if err != nil {
				return err
			}
			notifier := events.GitHubNotifier{BaseURL: connection.APIURL, RepositoryTokenSource: func(ctx context.Context, repository string) (string, error) {
				return a.eventConfig.GitHubApps.RepositoryToken(ctx, trigger.GitHubAppID, repository)
			}}
			_, err = notifier.UpdateComment(ctx, trigger.Repository, trigger.PullRequestNumber, trigger.ReportCommentID,
				workflowPreviewReportForTrigger(revision, resource, stages, trigger.PreviewURL, connection.WebURL, trigger))
			if err != nil {
				return err
			}
			return a.store.MarkWorkflowPreviewLifetimeReported(ctx, trigger, resource.State)
		}
		// The next completed deployment will publish the initial report.
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			return err
		}
		return a.store.MarkWorkflowPreviewLifetimeReported(ctx, trigger, resource.State)
	}
	return nil
}

func (a *API) reportPendingWorkflowPreviewLifetimes(ctx context.Context) error {
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, trigger := range triggers {
		if trigger.ClosedAt == nil && trigger.LifetimeReportPending {
			joined = errors.Join(joined, a.refreshWorkflowPreviewLifetimeReport(ctx, trigger.ID))
		}
	}
	return joined
}

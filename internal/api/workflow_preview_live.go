package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
)

// A live command changes only the matching preview instance. It does not
// create an instance or start a run; the next head update uses the new mode.
func (a *API) processWorkflowPreviewLiveComment(ctx context.Context, target *previewPollTarget, event core.IncomingEvent) error {
	fields := strings.Fields(event.Arguments)
	if len(fields) != 2 || (fields[1] != "on" && fields[1] != "off") {
		return fmt.Errorf("use %s live on or %s live off", event.Command, event.Command)
	}
	if _, err := strconv.ParseUint(event.SourceCommentID, 10, 64); err != nil {
		return fmt.Errorf("invalid GitHub comment ID: %w", err)
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	for _, trigger := range triggers {
		if trigger.ClosedAt != nil || trigger.GitHubAppID != target.connectionID || events.NormalizeRepository(trigger.Repository) != target.repository || trigger.PullRequestNumber != event.PullRequestNumber || trigger.Command != event.Command {
			continue
		}
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			return err
		}
		if !resource.Active || !resource.Temporary || resource.State == "expired" || resource.State == "expiring" {
			return fmt.Errorf("preview is %s; post %s to deploy it again", resource.State, event.Command)
		}
		latest, err := a.store.LatestWorkflowPreviewDeployComment(ctx, trigger.ID)
		if err != nil {
			return err
		}
		commentID, _ := strconv.ParseUint(event.SourceCommentID, 10, 64)
		latestID, _ := strconv.ParseUint(latest, 10, 64)
		if latestID > commentID {
			return a.workflowPreviewActivity(ctx, target, event, trigger, resource, "", "superseded", "A newer preview comment replaced this command.")
		}
		updated, err := a.store.UpdateWorkflowPreviewLiveReload(ctx, trigger.ID, event.SourceCommentID, fields[1] == "on")
		if err != nil {
			return err
		}
		if !updated {
			return a.workflowPreviewActivity(ctx, target, event, trigger, resource, "", "superseded", "A newer live reload command already set the update mode.")
		}
		trigger.LiveReload = fields[1] == "on"
		for index := range target.workflowTriggers {
			if target.workflowTriggers[index].ID == trigger.ID {
				target.workflowTriggers[index] = trigger
			}
		}
		if err := a.refreshWorkflowPreviewLifetimeReport(ctx, trigger.ID); err != nil && a.logger != nil {
			a.logger.Warn("preview report pending", "error", err)
		}
		message := "Live reload enabled. New commits will start a deployment when detected."
		if !trigger.LiveReload {
			message = "Live reload disabled. The preview's configured commit update policy is active."
		}
		return a.workflowPreviewActivity(ctx, target, event, trigger, resource, "", "processed", message)
	}
	return fmt.Errorf("no preview exists for this PR; post %s first", event.Command)
}

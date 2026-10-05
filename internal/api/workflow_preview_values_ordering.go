package api

import (
	"context"
	"strconv"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
)

// A rejected YAML edit must not change an accepted command, and an older
// rejected comment must stop blocking polling after a newer command repairs it.
func (a *API) workflowPreviewValuesErrorIsObsolete(ctx context.Context, target *previewPollTarget, event core.IncomingEvent) (bool, error) {
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return false, err
	}
	for _, trigger := range triggers {
		if trigger.GitHubAppID != target.connectionID || trigger.Command != event.Command {
			continue
		}
		matches := events.NormalizeRepository(trigger.Repository) == target.repository && trigger.PullRequestNumber == event.PullRequestNumber
		if !matches {
			resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
			if err != nil {
				return false, err
			}
			matches = previewTriggerLinks(trigger, resource)[target.repository] == event.PullRequestNumber
		}
		if !matches {
			continue
		}
		revisionID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, event.SourceCommentID)
		if err != nil {
			return false, err
		}
		if revisionID != "" {
			return true, nil
		}
		latestID, err := a.store.LatestWorkflowPreviewDeployComment(ctx, trigger.ID)
		if err != nil {
			return false, err
		}
		comment, commentErr := strconv.ParseUint(event.SourceCommentID, 10, 64)
		latest, latestErr := strconv.ParseUint(latestID, 10, 64)
		if commentErr == nil && latestErr == nil && comment > 0 && comment <= latest {
			return true, nil
		}
	}
	return false, nil
}

package api

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
)

func (a *API) processWorkflowPreviewTestComment(ctx context.Context, target *previewPollTarget, event core.IncomingEvent) error {
	for _, trigger := range target.workflowTriggers {
		if trigger.Command != event.Command || trigger.PullRequestNumber != event.PullRequestNumber {
			continue
		}
		reserved, err := a.store.ReserveWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID)
		if err != nil {
			return err
		}
		if !reserved {
			revisionID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, event.SourceCommentID)
			if err != nil {
				return err
			}
			if revisionID == "" {
				return nil
			}
			resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
			if err != nil {
				return err
			}
			return a.workflowPreviewRunActivity(ctx, target, event, trigger, resource, revisionID)
		}
		if strings.TrimSpace(event.Arguments) != "test" {
			return a.previewTestStartError(ctx, target, event, trigger, "Use `"+trigger.Command+" test` without extra arguments.")
		}
		revision, err := a.workflows.StartPreviewChecks(ctx, trigger.ResourceID, event.SourceCommentID)
		if err != nil {
			return a.previewTestStartError(ctx, target, event, trigger, err.Error())
		}
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			return err
		}
		if err := a.store.CompleteWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID, revision.ID); err != nil {
			return err
		}
		if err := a.workflowPreviewRunActivity(ctx, target, event, trigger, resource, revision.ID); err != nil {
			return err
		}

		if a.eventConfig.GitHubApps != nil {
			a.previewReportMu.Lock()
			reportErr := a.reportWorkflowFeedback(ctx, trigger.ResourceID)
			a.previewReportMu.Unlock()
			if reportErr != nil && a.logger != nil {
				a.logger.Warn("preview QA feedback pending", "error", reportErr)
			}
		}
		body := fmt.Sprintf("<!-- dispatch-preview-test:%s -->\n### Preview checks running\n\nChecks are running against [the deployed preview](%s). This command does not rebuild or redeploy it.\n", revision.ID, trigger.PreviewURL)
		commentID, err := a.postPreviewTestReply(ctx, trigger, "", body)
		if err != nil {
			return err
		}
		return a.store.UpdateWorkflowPreviewTestComment(ctx, trigger.ID, event.SourceCommentID, commentID)
	}
	// A test command never creates a preview instance.
	return nil
}

func (a *API) previewTestStartError(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, trigger core.WorkflowPreviewTrigger, detail string) error {
	sourceCommentID := event.SourceCommentID
	commentID, err := a.postPreviewTestReply(ctx, trigger, "", "Preview checks could not start: "+detail)
	if err != nil {
		_ = a.store.ReleaseWorkflowPreviewComment(ctx, trigger.ID, sourceCommentID)
		return err
	}
	if err := a.store.UpdateWorkflowPreviewTestComment(ctx, trigger.ID, sourceCommentID, commentID); err != nil {
		return err
	}
	return a.previewDeliveryActivity(ctx, target, event, "rejected", errors.New(detail))
}

func (a *API) postPreviewTestReply(ctx context.Context, trigger core.WorkflowPreviewTrigger, commentID, body string) (string, error) {
	connection, err := a.store.GetGitHubApp(ctx, trigger.GitHubAppID)
	if err != nil {
		return "", err
	}
	notifier := events.GitHubNotifier{BaseURL: connection.APIURL, Token: a.eventConfig.GitHubToken}
	if a.eventConfig.GitHubApps != nil {
		notifier.RepositoryTokenSource = func(ctx context.Context, repository string) (string, error) {
			return a.eventConfig.GitHubApps.RepositoryToken(ctx, trigger.GitHubAppID, repository)
		}
	}
	return postWorkflowPreviewComment(ctx, notifier, a.eventConfig.GitHubToken, trigger.Repository, trigger.PullRequestNumber, commentID, body)
}

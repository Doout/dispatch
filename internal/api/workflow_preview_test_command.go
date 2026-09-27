package api

import (
	"context"
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
		if err != nil || !reserved {
			return err
		}
		if strings.TrimSpace(event.Arguments) != "test" {
			return a.previewTestStartError(ctx, trigger, event.SourceCommentID, "Use `"+trigger.Command+" test` without extra arguments.")
		}
		revision, err := a.workflows.StartPreviewChecks(ctx, trigger.ResourceID, event.SourceCommentID)
		if err != nil {
			return a.previewTestStartError(ctx, trigger, event.SourceCommentID, err.Error())
		}
		if err := a.store.CompleteWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID, revision.ID); err != nil {
			return err
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

func (a *API) previewTestStartError(ctx context.Context, trigger core.WorkflowPreviewTrigger, sourceCommentID, detail string) error {
	commentID, err := a.postPreviewTestReply(ctx, trigger, "", "Preview checks could not start: "+detail)
	if err != nil {
		_ = a.store.ReleaseWorkflowPreviewComment(ctx, trigger.ID, sourceCommentID)
		return err
	}
	return a.store.UpdateWorkflowPreviewTestComment(ctx, trigger.ID, sourceCommentID, commentID)
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

package api

import (
	"context"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestWorkflowPreviewValuesDeletedCommentDoesNotReadOrDeploy(t *testing.T) {
	// No store or other dependencies are configured: any read, resolver or
	// workload call would fail before the deleted event could return.
	a := &API{}
	for _, body := range []string{
		previewGatewayValuesComment,
		"/preview values clear",
		"/preview values\n```yaml\nsetting: [broken\n```",
		"/preview",
	} {
		event := previewValuesEvent(t, "900", body)
		event.Action = "deleted"
		if err := a.processWorkflowPreviewWebhook(context.Background(), event); err != nil {
			t.Fatalf("deleted comment must not reach workflow processing: %v", err)
		}
		result, err := a.consumeGitHubPreviewEvent(context.Background(), event, nil, nil)
		if err != nil || !result.Ignored || result.Event.Kind != core.EventKindPullRequestComment || result.Event.SourceCommentID != event.SourceCommentID {
			t.Fatalf("deleted comment must not reach any preview service: result=%#v err=%v", result, err)
		}
	}
}

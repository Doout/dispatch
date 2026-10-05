package api

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
)

func TestWorkflowPreviewValuesMalformedCommentOrdering(t *testing.T) {
	f := newFeedbackFixture(t)
	a, ctx := f.a, context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	resource, err := a.store.GetWorkflowResource(ctx, "qa-preview")
	must(err)
	resource.Document, resource.Path = previewValuesDocument, "temporary/preview.yaml"
	must(a.store.UpdateWorkflowResource(ctx, resource))
	a.workflows.ResolvePreviewSource = func(_ context.Context, _ string, repository string, _ int) (githubapp.PullRequestHead, error) {
		return fixturePreviewSource(repository, strings.Repeat("a", 40)), nil
	}
	trigger := core.WorkflowPreviewTrigger{ID: "values-ordering", ResourceID: resource.ID, GitHubAppID: "qa-app", Repository: "example/service", PullRequestNumber: 42, Command: "/preview", CreatedAt: time.Now().UTC()}
	must(a.store.CreateWorkflowPreviewTrigger(ctx, trigger))
	target := &previewPollTarget{connectionID: trigger.GitHubAppID, repository: trigger.Repository, workflowTriggers: []core.WorkflowPreviewTrigger{trigger}, transport: "poll"}
	process := func(id, body string) error {
		return a.processWorkflowPreviewComment(ctx, target, previewValuesEvent(t, id, body), events.GitHubResolver{})
	}
	malformed := "/preview values\n```yaml\nsetting: [broken\n```"
	must(process("700", previewGatewayValuesComment))
	firstID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "700")
	must(err)
	if firstID == "" {
		t.Fatal("initial values comment did not create a durable receipt")
	}
	first, err := a.store.GetWorkflowRevision(ctx, firstID)
	must(err)
	// Editing an accepted comment cannot replace its values or its receipt.
	must(process("700", malformed))
	if err := process("701", malformed); err == nil || !strings.Contains(err.Error(), "one YAML mapping") {
		t.Fatalf("a new malformed comment must report its validation error: %v", err)
	}
	failedID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "701")
	must(err)
	if failedID != "" {
		t.Fatal("malformed comment reserved a workflow revision")
	}
	// A later clear repairs the preview. Re-reading the bad comment must no
	// longer keep the polling cursor in a failed scan.
	must(process("702", "/preview values clear"))
	must(process("701", malformed))
	clearID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "702")
	must(err)
	cleared, err := a.store.GetWorkflowRevision(ctx, clearID)
	must(err)
	if len(cleared.PreviewValues) != 0 {
		t.Fatal("superseded malformed replay restored cleared values")
	}
	// Lifetime-only receipts also make older or edited commands obsolete.
	must(process("703", "/preview ttl 1h"))
	must(process("703", malformed))
	must(process("701", malformed))
	// Cleanup can close the binding before an edited webhook is delivered.
	must(a.store.CloseWorkflowPreviewTrigger(ctx, trigger.ID, time.Now().UTC()))
	must(process("700", malformed))
	must(process("701", malformed))
	historical, err := a.store.GetWorkflowRevision(ctx, firstID)
	must(err)
	if len(first.PreviewValues["optimization"]) == 0 || !reflect.DeepEqual(historical.PreviewValues, first.PreviewValues) {
		t.Fatal("malformed replays changed the historical values snapshot")
	}
	runs, err := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
	must(err)
	for _, run := range runs {
		if strings.HasPrefix(run.Trigger, "pull request comment ") && run.ID != firstID && run.ID != clearID {
			t.Fatalf("a malformed replay started another revision: %+v", run)
		}
	}
}

func TestWorkflowPreviewValuesUntrustedCommentsSkipLegacyFallback(t *testing.T) {
	f := newFeedbackFixture(t)
	for _, body := range []string{
		previewGatewayValuesComment,
		"/preview values clear",
		"/preview values\n```yaml\nsetting: [broken\n```",
	} {
		event := previewValuesEvent(t, "800", body)
		event.TrustedActor = false
		// Missing services would fail if the comment reached groups or legacy
		// deployment processing after the workflow trust filter ignored it.
		result, err := f.a.consumeGitHubPreviewEvent(context.Background(), event, nil, nil)
		if err != nil || result.Event.SourceCommentID != event.SourceCommentID {
			t.Fatalf("untrusted values comment reached fallback or failed: result=%#v err=%v", result, err)
		}
	}
}

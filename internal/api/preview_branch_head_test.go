package api

import (
	"context"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestPreviewPollingTracksBranchWhenPRMetadataIsStale(t *testing.T) {
	ctx := context.Background()
	a, fake, template := previewPanelFixture(t)
	_, resolver, _, _, err := a.previewPanelGitHub(ctx, "github", "example/service")
	if err != nil {
		t.Fatal(err)
	}
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	event := core.IncomingEvent{Command: "/preview", Arguments: "with ui=#84", PullRequestNumber: 42, HeadSHA: strings.Repeat("a", 40), SourceCommentID: "900", TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	trigger := target.workflowTriggers[0]
	trigger.LiveReload = true
	if err := a.store.UpdateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	assertRuns := func(count int, service, ui string) {
		t.Helper()
		runs, err := a.store.ListWorkflowRevisions(ctx, trigger.ResourceID, 0)
		if err != nil || len(runs) != count {
			t.Fatalf("runs=%d want %d: %v", len(runs), count, err)
		}
		if runs[0].Sources["service"].CommitSHA != service || runs[0].Sources["ui"].CommitSHA != ui {
			t.Fatalf("wrong branch commits: %+v", runs[0].Sources)
		}
	}
	oldService, oldUI := strings.Repeat("a", 40), strings.Repeat("b", 40)
	assertRuns(1, oldService, oldUI)
	// Neither PR metadata SHA nor updated_at changes. No webhook or new comment
	// arrives, but both actual head branches advance.
	newService, newUI := strings.Repeat("c", 40), strings.Repeat("d", 40)
	fake.mu.Lock()
	fake.branchHeads = map[string]string{"example/service": newService, "example/ui": newUI}
	fake.mu.Unlock()
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuns(2, newService, newUI)
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuns(2, newService, newUI) // Polling both linked repos must not duplicate the run.
	// Force-push the UI branch back while advancing the primary source.
	newService = strings.Repeat("e", 40)
	fake.mu.Lock()
	fake.branchHeads = map[string]string{"example/service": newService, "example/ui": oldUI}
	fake.mu.Unlock()
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertRuns(3, newService, oldUI)
	fake.mu.Lock()
	fake.branchFailure = true
	fake.mu.Unlock()
	if err := a.PollPreviewsOnce(ctx); err == nil {
		t.Fatal("failed branch reads silently reused stale PR metadata")
	}
	assertRuns(3, newService, oldUI)
	fake.mu.Lock()
	fake.branchFailure = false
	fake.branchHeads["example/service"] = strings.Repeat("f", 40)
	fake.mu.Unlock()
	// Exercise the next scan directly instead of sleeping through retry backoff.
	targets, err := a.previewPollTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if err := a.scanPreviewTarget(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
	assertRuns(4, strings.Repeat("f", 40), oldUI)
	// An old synchronize delivery must not pin an older commit afterwards.
	if err := a.updateWorkflowPreviewHead(ctx, trigger, "example/service", oldService, resolver, "webhook"); err != nil {
		t.Fatal(err)
	}
	assertRuns(4, strings.Repeat("f", 40), oldUI)
}

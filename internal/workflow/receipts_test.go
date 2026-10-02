package workflow

import (
	"context"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestReceivedWorkflowCommandCannotCancelOrStartAnotherRunAfterRestart(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	command := store.WithWorkflowReceipt(ctx, store.WorkflowReceipt{Key: "github-event:accepted"})
	r := core.WorkflowRevision{ID: "already-accepted", ResourceID: f.resource.ID, State: "failed", Trigger: "github push", CreatedAt: time.Now().UTC()}
	if err := f.data.CreateWorkflowRevision(command, r); err != nil {
		t.Fatal(err)
	}
	// A later preview run must remain intact even when an older command retries.
	newer := core.WorkflowRevision{ID: "newer-run", ResourceID: f.resource.ID, State: "awaiting_approval", CreatedAt: time.Now().UTC()}
	if err := f.data.CreateWorkflowRevision(ctx, newer); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Store: f.data}
	for _, start := range []func() (core.WorkflowRevision, error){
		func() (core.WorkflowRevision, error) {
			return restarted.Start(command, f.resource.ID, "pull request comment 42")
		},
		func() (core.WorkflowRevision, error) {
			return restarted.StartPreviewChecks(command, f.resource.ID, "42")
		},
		func() (core.WorkflowRevision, error) {
			return restarted.startIfChanged(command, f.resource, f.source, f.snapshot, "github push")
		},
	} {
		saved, err := start()
		if err != nil || saved.ID != r.ID {
			t.Fatalf("replayed command: %+v %v", saved, err)
		}
	}
	saved, err := f.data.GetWorkflowRevision(ctx, newer.ID)
	if err != nil || saved.State != "awaiting_approval" {
		t.Fatalf("retry cancelled newer work: %+v %v", saved, err)
	}
	items, err := f.data.ListWorkflowRevisions(ctx, f.resource.ID, 0)
	if err != nil || len(items) != 3 {
		t.Fatalf("duplicate revisions: %+v %v", items, err)
	}
}

func TestWebhookNoChangeReceiptRemainsNoChangeWhenLaterInputsDiffer(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := store.WithWorkflowReceipt(context.Background(), store.WorkflowReceipt{Key: "github-event:no-change"})
	first, err := f.service.startIfChanged(ctx, f.resource, f.source, f.previous.Sources, "github push")
	if err != nil || first.ID != "" {
		t.Fatalf("unchanged inputs: %+v %v", first, err)
	}
	restarted := &Service{Store: f.data}
	second, err := restarted.startIfChanged(ctx, f.resource, f.source, f.snapshot, "github push")
	if err != nil || second.ID != "" || second.State != "no_change" {
		t.Fatalf("old event started changed inputs: %+v %v", second, err)
	}
}

func TestPushHandlerOnlyPersistsIntentAndRecoveryDrainsQueuedWork(t *testing.T) {
	data, source, resource := workflowRunnerFixture(t, "durable-push")
	ctx := context.Background()
	source.SyncMode = core.ConfigSyncWebhook
	if err := data.UpdateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	resource.Active = false
	if err := data.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: data}
	body := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository":{"full_name":"example/config"}}`)
	count, err := s.HandlePush(ctx, source.GitHubAppID, "delivery", body)
	if err != nil || count != 1 {
		t.Fatalf("push acceptance: %d %v", count, err)
	}
	items, err := data.PendingWorkflowEvents(ctx)
	if err != nil || len(items) != 1 || items[0].State != "queued" {
		t.Fatalf("push did work before drain: %+v %v", items, err)
	}
	if count, err := s.HandlePush(ctx, source.GitHubAppID, "delivery", body); err != nil || count != 0 {
		t.Fatalf("duplicate push intent: %d %v", count, err)
	}
	// Configuration is paused before recovery: the queued trigger is consumed
	// without fetching credentials or reviving the paused application.
	source.Active = false
	if err := data.UpdateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	restarted := &Service{Store: data}
	if err := restarted.RecoverPushEvents(ctx); err != nil {
		t.Fatal(err)
	}
	items, err = data.PendingWorkflowEvents(ctx)
	if err != nil || len(items) != 0 {
		t.Fatalf("recovery left queue stranded: %+v %v", items, err)
	}
	saved, err := data.WorkflowEventsForDelivery(ctx, "delivery")
	if err != nil || len(saved) != 1 || saved[0].State != "processed" {
		t.Fatalf("durable processing outcome: %+v %v", saved, err)
	}
}

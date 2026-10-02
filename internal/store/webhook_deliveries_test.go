package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestWebhookReceiptsSurviveRestartRetentionAndConcurrentDelivery(t *testing.T) {
	testWebhookReceipts(t, filepath.Join(t.TempDir(), "deliveries.db"))
}
func TestWebhookReceiptsPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL database required")
	}
	testWebhookReceipts(t, dsn)
}
func testWebhookReceipts(t *testing.T, dsn string) {
	ctx := context.Background()
	s := mutationStore(t, dsn)
	now := time.Now().UTC()
	suffix := ulid.Make().String()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	project := "webhook-" + suffix
	must(s.CreateProject(ctx, core.Project{ID: project, Name: project, CreatedAt: now}))
	r := core.WebhookDelivery{ID: ulid.Make().String(), ConnectionID: suffix, DeliveryID: "delivery", BodyDigest: "digest", Event: "issue_comment", Repository: "acme/app", ReceivedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), Ciphertext: "encrypted-payload", Activities: []core.EventActivity{{ID: ulid.Make().String(), ProjectID: project, RuleID: "rule", Transport: "webhook", Kind: "webhook_delivery", CreatedAt: now}}}
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := core.WebhookDelivery{ID: ulid.Make().String(), ConnectionID: suffix, DeliveryID: "delivery", BodyDigest: "digest", Event: "issue_comment", Repository: "acme/app", ReceivedAt: now, ExpiresAt: now.Add(7 * 24 * time.Hour), Ciphertext: "encrypted-payload", Activities: []core.EventActivity{{ID: ulid.Make().String(), ProjectID: project, RuleID: "rule", Transport: "webhook", Kind: "webhook_delivery", CreatedAt: now}}}
			candidate.ID = ulid.Make().String()
			saved, created, err := s.AcceptWebhookDelivery(ctx, candidate)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if created {
				winners++
				r.ID = saved.ID
			}
		}()
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("accepted %d commands", winners)
	}
	changed := r
	changed.BodyDigest = "different"
	if _, _, err := s.AcceptWebhookDelivery(ctx, changed); !errors.Is(err, ErrWebhookDeliveryConflict) {
		t.Fatalf("conflict: %v", err)
	}
	changed = r
	changed.ID = ulid.Make().String()
	changed.DeliveryID = "unsigned-new-header"
	if saved, created, err := s.AcceptWebhookDelivery(ctx, changed); err != nil || created || saved.ID != r.ID {
		t.Fatalf("signed body replay: %+v %t %v", saved, created, err)
	}
	claim, err := s.ClaimWebhookDelivery(ctx, now.Add(time.Second), time.Minute)
	must(err)
	if claim.Attempts != 1 || claim.State != "running" {
		t.Fatalf("claim: %+v", claim)
	}
	if _, err := s.ClaimWebhookDelivery(ctx, now.Add(2*time.Second), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("concurrent worker claim: %v", err)
	}
	// Reopen a store while an interrupted worker still owns its expired token.
	reopened, err := Open(ctx, dsn)
	must(err)
	defer reopened.Close()
	second, err := reopened.ClaimWebhookDelivery(ctx, now.Add(2*time.Minute), time.Minute)
	must(err)
	if second.ID != claim.ID || second.Attempts != 2 || second.LeaseToken == claim.LeaseToken {
		t.Fatalf("restart claim: %+v", second)
	}
	claim.State = "processed"
	if err := s.FinishWebhookDelivery(ctx, claim, now.Add(2*time.Minute)); err == nil {
		t.Fatal("stale attempt overwrote new worker")
	}
	second.State = "retry"
	second.Error = "Safe retry reason"
	second.NextAttemptAt = now.Add(4 * time.Minute)
	must(reopened.FinishWebhookDelivery(ctx, second, now.Add(2*time.Minute)))
	activity, err := s.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{project}})
	must(err)
	if len(activity) != 1 || activity[0].Attempts != 2 || activity[0].State != "retry" || activity[0].NextAttemptAt == nil {
		t.Fatalf("retry visibility: %+v", activity)
	}
	third, err := s.ClaimWebhookDelivery(ctx, now.Add(4*time.Minute), time.Minute)
	must(err)
	third.State = "processed"
	third.Result = `{"revisionIds":["run"]}`
	must(s.FinishWebhookDelivery(ctx, third, now.Add(4*time.Minute)))
	items, err := s.ListWebhookDeliveries(ctx, "", 50)
	must(err)
	if len(items) != 1 || items[0].Ciphertext != "" || items[0].State != "processed" {
		t.Fatalf("terminal payload: %+v", items)
	}
	// Activity expiration never erases the dedupe record or independent poll progress.
	_, err = s.ProcessIncomingEvent(ctx, core.IncomingEvent{ID: "event-" + suffix, Provider: core.EventProviderGitHub, DeliveryID: "logical-" + suffix, Kind: core.EventKindPullRequest, Action: "closed", Repository: "acme/app", PullRequestNumber: 42, ReceivedAt: now, ProviderConnectionID: suffix})
	must(err)
	must(s.SavePreviewPollCursor(ctx, suffix, "acme/app", now))
	must(s.RetainWebhookDeliveries(ctx, now.Add(40*24*time.Hour)))
	activity, err = s.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{project}})
	must(err)
	if len(activity) != 0 {
		t.Fatalf("old activity retained: %+v", activity)
	}
	if saved, created, err := s.AcceptWebhookDelivery(ctx, changed); err != nil || created || saved.ID != r.ID {
		t.Fatalf("retention replay: %+v %t %v", saved, created, err)
	}
	exists, err := s.IncomingEventExists(ctx, core.EventProviderGitHub, "logical-"+suffix)
	must(err)
	if !exists {
		t.Fatal("close watermark removed")
	}
	cursor, err := s.PreviewPollCursor(ctx, suffix, "acme/app")
	must(err)
	if cursor == nil || !cursor.Equal(now) {
		t.Fatal("retention changed poll cursor")
	}
}

func TestWebhookExpiryErasesPayloadAndNeverRequeues(t *testing.T) {
	ctx := context.Background()
	s := mutationStore(t, filepath.Join(t.TempDir(), "expiry.db"))
	now := time.Now().UTC()
	r := core.WebhookDelivery{ID: "old", ConnectionID: "app", DeliveryID: "old", BodyDigest: "old", Event: "push", ReceivedAt: now.Add(-8 * 24 * time.Hour), ExpiresAt: now.Add(-time.Hour), Ciphertext: "private"}
	if _, _, err := s.AcceptWebhookDelivery(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.RetainWebhookDeliveries(ctx, now); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListWebhookDeliveries(ctx, "", 50)
	if err != nil || len(items) != 1 || items[0].State != "expired" || items[0].Ciphertext != "" {
		t.Fatalf("expiry: %+v %v", items, err)
	}
	if _, err := s.ClaimWebhookDelivery(ctx, now, time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired receipt resumed: %v", err)
	}
}

func TestWorkflowReceiptAtomicallyBindsCommentAndProtectsRetainedRun(t *testing.T) {
	ctx := context.Background()
	s := mutationStore(t, filepath.Join(t.TempDir(), "commands.db"))
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.CreateProject(ctx, core.Project{ID: "project", Name: "project", CreatedAt: now}))
	must(s.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "github", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", GitHubAppID: "github", Name: "source", Repository: "acme/app", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "source", Name: "preview", Kind: "Application", Temporary: true, CreatedAt: now, UpdatedAt: now}))
	must(s.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "resource", GitHubAppID: "github", Repository: "acme/app", PullRequestNumber: 1, Command: "/preview", CreatedAt: now}))
	for i := 0; i < 2; i++ {
		reserved, err := s.ReserveWorkflowPreviewComment(ctx, "trigger", "42")
		must(err)
		if !reserved {
			t.Fatal("unfinished reservation was stranded")
		}
	}
	command := WithWorkflowReceipt(ctx, WorkflowReceipt{Key: "preview-comment:trigger:42", TriggerID: "trigger", CommentID: "42"})
	r := core.WorkflowRevision{ID: "accepted-run", ResourceID: "resource", Trigger: "pull request comment 42", State: "queued", CreatedAt: now}
	must(s.CreateWorkflowRevision(command, r))
	linked, err := s.WorkflowPreviewCommentRevision(ctx, "trigger", "42")
	must(err)
	if linked != r.ID {
		t.Fatal("revision committed without command link")
	}
	r.ID = "duplicate-run"
	if err := s.CreateWorkflowRevision(command, r); err == nil {
		t.Fatal("replayed command created a second run")
	}
	if _, err := s.GetWorkflowRevision(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("partial duplicate revision committed")
	}
	// The command receipt outlives optional workflow history retention.
	_, err = s.db.ExecContext(ctx, `DELETE FROM workflow_revisions WHERE id='accepted-run'`)
	must(err)
	replayed, err := s.WorkflowReceiptRevision(ctx, "resource", "preview-comment:trigger:42")
	must(err)
	if replayed.ID != "accepted-run" || replayed.State != "retained_receipt" {
		t.Fatalf("retained command forgotten: %+v", replayed)
	}
	bad := WithWorkflowReceipt(ctx, WorkflowReceipt{Key: "missing-reservation", TriggerID: "trigger", CommentID: "99"})
	r.ID = "orphan-run"
	if err := s.CreateWorkflowRevision(bad, r); err == nil {
		t.Fatal("run accepted without matching command reservation")
	}
	if _, err := s.WorkflowReceiptRevision(ctx, "resource", "missing-reservation"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed transaction retained receipt: %v", err)
	}
}

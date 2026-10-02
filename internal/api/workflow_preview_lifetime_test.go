package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

type lifetimeCleanupRecorder struct {
	previewCleanupRecorder
	failure error
	count   int
}

func (e *lifetimeCleanupRecorder) Cleanup(ctx context.Context, app core.App, server core.Server, progress deploy.Progress) error {
	e.count++
	if e.failure != nil {
		return e.failure
	}
	return e.previewCleanupRecorder.Cleanup(ctx, app, server, progress)
}

func previewLifetimeFixture(t *testing.T, ttl string, deadline *time.Time) (*API, *store.SQLStore, *lifetimeCleanupRecorder, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lifetime.db")
	data, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, create := range []func() error{
		func() error {
			return data.CreateProject(ctx, core.Project{ID: "project", Name: "Preview", CreatedAt: now})
		},
		func() error {
			return data.CreateServer(ctx, core.Server{ID: "server", Name: "Target", Runtime: core.ServerRuntimeKubernetes, State: "ready", CreatedAt: now})
		},
		func() error {
			return data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: "https://github.example", APIURL: "https://github.example/api/v3", State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: "project", GitHubAppID: "github", Name: "Config", Repository: "example/devops", Active: true, CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "config", Kind: "Application", Name: "preview-42", Path: "temporary.yaml", Document: "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview-42\nspec:\n  sources:\n    service:\n      repository: example/service\n      ref: " + strings.Repeat("a", 40) + "\n", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "resource", GitHubAppID: "github", Repository: "example/service", PullRequestNumber: 42, Command: "/preview", TTL: ttl, CreatedAt: now})
		},
		func() error {
			return data.CreateApp(ctx, core.App{ID: "current", ProjectID: "project", ServerID: "server", Name: "Current", BuildType: core.BuildTypeHelm, State: "ready", Generated: true, HelmProvenance: core.HelmProvenance{WorkflowResourceID: "resource"}, CreatedAt: now})
		},
		func() error {
			return data.CreateApp(ctx, core.App{ID: "older", ProjectID: "project", ServerID: "server", Name: "Older", BuildType: core.BuildTypeHelm, State: "ready", Generated: true, CreatedAt: now})
		},
		func() error {
			return data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "revision", ResourceID: "resource", State: "queued", CreatedAt: now})
		},
		func() error {
			return data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "stage", RevisionID: "revision", StageName: "development", State: "running", DeploymentResults: []core.WorkflowDeploymentResult{{AppID: "older"}}, CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	if deadline != nil {
		triggers, err := data.ListWorkflowPreviewTriggers(ctx)
		if err != nil {
			t.Fatal(err)
		}
		previous, next := triggers[0], triggers[0]
		next.ExpiresAt = deadline
		if _, err = data.SaveWorkflowPreviewLifetime(ctx, previous, next, "fixture-lifetime", now); err != nil {
			t.Fatal(err)
		}
	}
	executor := &lifetimeCleanupRecorder{previewCleanupRecorder: previewCleanupRecorder{cleaned: map[string]bool{}}}
	a := New(data, deploy.NewService(data, executor), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.workflows.ResolvePreviewSource = func(_ context.Context, _ string, repository string, _ int) (githubapp.PullRequestHead, error) {
		return fixturePreviewSource(repository, strings.Repeat("a", 40)), nil
	}
	return a, data, executor, path
}

func TestPreviewLiveCommandsChangeModeWithoutDeploying(t *testing.T) {
	ctx := context.Background()
	a, data, _, _ := previewLifetimeFixture(t, "0", nil)
	target := &previewPollTarget{connectionID: "github", repository: "example/service"}
	event := core.IncomingEvent{Command: "/preview", Arguments: "live on", Repository: "example/service", PullRequestNumber: 42, SourceCommentID: "120", TrustedActor: true}
	for _, step := range []struct {
		id      string
		args    string
		enabled bool
	}{{"120", "live on", true}, {"119", "live off", true}, {"121", "live off", false}, {"122", "live on", true}} {
		event.SourceCommentID, event.Arguments = step.id, step.args
		if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
			t.Fatal(err)
		}
		triggers, err := data.ListWorkflowPreviewTriggers(ctx)
		if err != nil || len(triggers) != 1 || triggers[0].LiveReload != step.enabled {
			t.Fatalf("command %s changed mode incorrectly: %+v, %v", step.args, triggers, err)
		}
		revisions, err := data.ListWorkflowRevisions(ctx, "resource", 0)
		if err != nil || len(revisions) != 1 {
			t.Fatalf("mode command started deployment: %+v, %v", revisions, err)
		}
	}
}

func TestPreviewExpiryCleansHelmHistoryAndRetriesWithoutGitHub(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	a, data, executor, _ := previewLifetimeFixture(t, "1d", &now)
	if err := a.expireWorkflowPreviews(ctx, now.Add(-time.Minute)); err != nil || executor.count != 0 {
		t.Fatalf("cleaned before expiry: %d, %v", executor.count, err)
	}
	executor.failure = errors.New("target offline")
	if err := a.expireWorkflowPreviews(ctx, now); err == nil {
		t.Fatal("cleanup failure was hidden")
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.Active || resource.State != "expiring" || !strings.Contains(resource.LastError, "target offline") {
		t.Fatalf("cleanup failure was not persisted: %+v, %v", resource, err)
	}
	if _, _, err := a.workflows.Activate(ctx, "resource"); err == nil {
		t.Fatal("activated during cleanup")
	}
	executor.failure = nil
	if err := a.expireWorkflowPreviews(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	resource, err = data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.Active || resource.State != "expired" || resource.LastError != "" {
		t.Fatalf("did not expire: %+v, %v", resource, err)
	}
	if !executor.cleaned["current"] || !executor.cleaned["older"] {
		t.Fatalf("missed Helm history: %+v", executor.cleaned)
	}
	revision, err := data.GetWorkflowRevision(ctx, "revision")
	if err != nil || revision.State != "cancelled" {
		t.Fatalf("pending work survived expiry: %+v, %v", revision, err)
	}
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || triggers[0].ClosedAt != nil {
		t.Fatalf("expiry lost the PR binding: %+v, %v", triggers, err)
	}
	count := executor.count
	if err := a.expireWorkflowPreviews(ctx, now.Add(2*time.Minute)); err != nil || executor.count != count {
		t.Fatalf("repeated cleanup: %d, %v", executor.count, err)
	}
	if _, _, err := a.workflows.Activate(ctx, "resource"); err == nil {
		t.Fatal("UI activation bypassed expired lifetime")
	}
	// An old delivery cannot revive the preview; a new comment reuses its ID.
	if _, err := data.ReserveWorkflowPreviewComment(ctx, "trigger", "1"); err != nil {
		t.Fatal(err)
	}
	if err := data.CompleteWorkflowPreviewComment(ctx, "trigger", "1", "revision"); err != nil {
		t.Fatal(err)
	}
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTriggers: triggers}
	event := core.IncomingEvent{Repository: target.repository, PullRequestNumber: 42, Command: "/preview", SourceCommentID: "1", HeadSHA: strings.Repeat("a", 40)}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	resource, _ = data.GetWorkflowResource(ctx, "resource")
	if resource.Active {
		t.Fatal("replayed command revived expired deployment")
	}
	event.SourceCommentID = "2"
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	resource, _ = data.GetWorkflowResource(ctx, "resource")
	triggers, _ = data.ListWorkflowPreviewTriggers(ctx)
	if !resource.Active || len(triggers) != 1 || triggers[0].ExpiresAt == nil || !triggers[0].ExpiresAt.After(now.Add(23*time.Hour)) {
		t.Fatalf("new command did not renew same preview: %+v, %+v", resource, triggers)
	}
}

func TestPreviewLifetimeCommentsPersistAndDoNotRebuild(t *testing.T) {
	ctx := context.Background()
	a, data, executor, path := previewLifetimeFixture(t, "0", nil)
	target := &previewPollTarget{connectionID: "github", repository: "example/service"}
	event := core.IncomingEvent{Repository: target.repository, PullRequestNumber: 42, Command: "/preview", SourceCommentID: "10", Arguments: "ttl 1d"}
	before := time.Now().UTC()
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	triggers, _ := data.ListWorkflowPreviewTriggers(ctx)
	first := *triggers[0].ExpiresAt
	if triggers[0].TTL != "1d" || first.Before(before.Add(24*time.Hour)) || first.After(time.Now().Add(24*time.Hour)) {
		t.Fatalf("wrong deadline: %+v", triggers[0])
	}
	event.SourceCommentID, event.Arguments = "11", "extend 1d"
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	a = New(reopened, deploy.NewService(reopened, executor), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	triggers, _ = reopened.ListWorkflowPreviewTriggers(ctx)
	if triggers[0].ExpiresAt == nil || !triggers[0].ExpiresAt.Equal(first.Add(24*time.Hour)) {
		t.Fatalf("duplicate delivery extended twice: %+v", triggers[0])
	}
	event.SourceCommentID, event.Arguments = "12", "ttl 0"
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	triggers, _ = reopened.ListWorkflowPreviewTriggers(ctx)
	if triggers[0].TTL != "0" || triggers[0].ExpiresAt != nil {
		t.Fatal("time limit was not removed")
	}
	if err := a.expireWorkflowPreviews(ctx, before.Add(365*24*time.Hour)); err != nil || executor.count != 0 {
		t.Fatalf("unlimited preview was cleaned: %d, %v", executor.count, err)
	}
	revisions, _ := reopened.ListWorkflowRevisions(ctx, "resource", 0)
	if len(revisions) != 1 || revisions[0].State != "queued" {
		t.Fatalf("lifetime command ran or cancelled builds: %+v", revisions)
	}
	event.SourceCommentID, event.Arguments = "9", "ttl invalid"
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal("newer valid command did not supersede an older invalid command", err)
	}
	for i, arguments := range []string{"ttl -1d", "ttl", "extend 0", "extend 1d", "ttl 1d extra"} {
		event.SourceCommentID, event.Arguments = string(rune('a'+i)), arguments
		if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err == nil {
			t.Errorf("accepted %q", arguments)
		}
	}
}

func TestPreviewExpiryLeasePreventsRenewalDuringCleanup(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	_, data, _, _ := previewLifetimeFixture(t, "1d", &now)
	triggers, _ := data.ListWorkflowPreviewTriggers(ctx)
	previous := triggers[0]
	next := previous
	deadline := now.Add(time.Hour)
	next.ExpiresAt = &deadline
	if accepted, err := data.SaveWorkflowPreviewLifetime(ctx, previous, next, "20", now); err != nil || !accepted {
		t.Fatalf("extension failed: %v, %v", accepted, err)
	}
	if claimed, err := data.ClaimWorkflowPreviewExpiry(ctx, "trigger", now, now.Add(10*time.Minute)); err != nil || claimed {
		t.Fatalf("stale expiry defeated extension: %v, %v", claimed, err)
	}
	now = now.Add(2 * time.Hour)
	lease := now.Add(10 * time.Minute)
	if claimed, err := data.ClaimWorkflowPreviewExpiry(ctx, "trigger", now, lease); err != nil || !claimed {
		t.Fatalf("due cleanup not claimed: %v, %v", claimed, err)
	}
	if claimed, err := data.ClaimWorkflowPreviewExpiry(ctx, "trigger", now, lease.Add(time.Minute)); err != nil || claimed {
		t.Fatalf("concurrent cleanup claimed same preview: %v, %v", claimed, err)
	}
	triggers, _ = data.ListWorkflowPreviewTriggers(ctx)
	previous, next = triggers[0], triggers[0]
	next.TTL, next.ExpiresAt = "0", nil
	if _, err := data.SaveWorkflowPreviewLifetime(ctx, previous, next, "21", now); err == nil {
		t.Fatal("extension was accepted after cleanup began")
	}
	if handled, err := data.WorkflowPreviewLifetimeCommentHandled(ctx, "trigger", "21"); err != nil || handled {
		t.Fatalf("failed extension left a receipt: %v, %v", handled, err)
	}
	if claimed, err := data.ClaimWorkflowPreviewExpiry(ctx, "trigger", lease.Add(time.Second), lease.Add(time.Hour)); err != nil || !claimed {
		t.Fatalf("controller restart could not reclaim expired lease: %v, %v", claimed, err)
	}
}

func TestFailedPreviewCommandDoesNotKeepRenewingItsLifetime(t *testing.T) {
	ctx := context.Background()
	a, data, _, _ := previewLifetimeFixture(t, "1d", nil)
	triggers, _ := data.ListWorkflowPreviewTriggers(ctx)
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTriggers: triggers}
	// This fixture has no Git credentials. Source resolution fails before any
	// external request, leaving the comment available for a later retry.
	event := core.IncomingEvent{Repository: target.repository, PullRequestNumber: 42, Command: "/preview", SourceCommentID: "30", HeadSHA: "unresolved-ref"}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err == nil {
		t.Fatal("expected source resolution failure")
	}
	triggers, _ = data.ListWorkflowPreviewTriggers(ctx)
	deadline := triggers[0].ExpiresAt
	if deadline == nil {
		t.Fatal("accepted deployment command did not start lifetime")
	}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err == nil {
		t.Fatal("expected source resolution failure on retry")
	}
	triggers, _ = data.ListWorkflowPreviewTriggers(ctx)
	if !triggers[0].ExpiresAt.Equal(*deadline) {
		t.Fatal("a retry extended the deadline")
	}
	if err := a.expireWorkflowPreviews(ctx, deadline.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	resource, _ := data.GetWorkflowResource(ctx, "resource")
	if resource.Active || resource.State != "expired" {
		t.Fatal("failed comment replay revived expired preview")
	}
}

func TestPreviewExpiryClockRunsWithoutGitHubConfiguration(t *testing.T) {
	now := time.Now().UTC().Add(-time.Minute)
	a, data, executor, _ := previewLifetimeFixture(t, "1d", &now)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { a.RunPreviewExpirer(ctx); close(done) }()
	for {
		resource, err := data.GetWorkflowResource(ctx, "resource")
		if err != nil {
			t.Fatal(err)
		}
		if resource.State == "expired" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("expiry clock did not clean the preview")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	if !executor.cleaned["current"] || !executor.cleaned["older"] {
		t.Fatal("expiry clock missed Helm releases")
	}
}

func TestPRCloseKeepsPreviewUntilLinkedPullRequestsClose(t *testing.T) {
	ctx := context.Background()
	a, data, executor, _ := previewLifetimeFixture(t, "0", nil)
	resource, _ := data.GetWorkflowResource(ctx, "resource")
	resource.Document += "    ui:\n      repository: example/ui\n      ref: " + strings.Repeat("b", 40) + "\n"
	if err := data.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateWorkflowPreviewTriggerLinks(ctx, "trigger", map[string]int{"ui": 21}); err != nil {
		t.Fatal(err)
	}
	triggers, _ := data.ListWorkflowPreviewTriggers(ctx)
	var open atomic.Bool
	open.Store(true)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/example/service/pulls/42" {
			_, _ = io.WriteString(w, `{"state":"closed","head":{"sha":"abc","ref":"feature"}}`)
			return
		}
		if r.URL.Path != "/repos/example/ui/pulls/21" {
			t.Errorf("unexpected GitHub endpoint %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		state := "closed"
		if open.Load() {
			state = "open"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"number":21,"state":"`+state+`","head":{"ref":"feature","sha":"`+strings.Repeat("b", 40)+`"},"base":{"ref":"main"}}`)
	}))
	defer github.Close()
	resolver := events.GitHubResolver{BaseURL: github.URL}
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTriggers: triggers}
	event := core.IncomingEvent{Provider: core.EventProviderGitHub, Kind: core.EventKindPullRequest, Action: "closed", Repository: target.repository, PullRequestNumber: 42, DeliveryID: "close-42", ReceivedAt: time.Now().UTC()}
	if err := a.consumePolledClosure(ctx, target, event, resolver, a.groups, a.events); err != nil {
		t.Fatal(err)
	}
	if executor.count != 0 {
		t.Fatal("open linked PR did not preserve preview")
	}
	open.Store(false)
	if err := a.consumePolledClosure(ctx, target, event, resolver, a.groups, a.events); err != nil {
		t.Fatal(err)
	}
	if !executor.cleaned["current"] || !executor.cleaned["older"] {
		t.Fatal("last closed PR did not remove Helm releases")
	}
}

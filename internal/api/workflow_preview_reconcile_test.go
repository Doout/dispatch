package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
)

type partialPreviewCleanup struct {
	previewCleanupRecorder
	counts  map[string]int
	offline bool
}

func (e *partialPreviewCleanup) Cleanup(ctx context.Context, app core.App, server core.Server, p deploy.Progress) error {
	e.counts[app.ID]++
	if app.ID == "older" && e.offline {
		return errors.New("target unavailable")
	}
	return e.previewCleanupRecorder.Cleanup(ctx, app, server, p)
}
func TestPreviewCleanupRestartsWithoutGitHubOrRepeatingCompletedApps(t *testing.T) {
	ctx := context.Background()
	_, data, _, path := previewLifetimeFixture(t, "0", nil)
	executor := &partialPreviewCleanup{previewCleanupRecorder: previewCleanupRecorder{cleaned: map[string]bool{}}, counts: map[string]int{}, offline: true}
	a := New(data, deploy.NewService(data, executor), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil {
		t.Fatal(err)
	}
	intent, err := a.beginWorkflowPreviewCleanup(ctx, resource, "removed", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileWorkflowPreviewCleanup(ctx, intent.ID); err == nil {
		t.Fatal("partial failure hidden")
	}
	history, err := data.ListWorkflowPreviewCleanups(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].State != "blocked" || history[0].Apps[0].State != "succeeded" || history[0].Apps[1].State != "blocked" {
		t.Fatalf("partial outcomes lost: %+v", history)
	}
	previousJob := history[0].Apps[1].JobID
	if err = data.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	executor.offline = false
	a = New(reopened, deploy.NewService(reopened, executor), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// TTL is unlimited and GitHub is absent. The independent timer must still
	// resume the persisted explicit removal, including the original operation.
	if err = a.expireWorkflowPreviews(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	history, err = reopened.ListWorkflowPreviewCleanups(ctx, resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if history[0].State != "succeeded" || history[0].Apps[1].JobID != previousJob || executor.counts["current"] != 1 || executor.counts["older"] != 2 {
		t.Fatalf("restart repeated or lost work: %+v, %+v", history, executor.counts)
	}
	saved, _ := reopened.GetWorkflowResource(ctx, resource.ID)
	if saved.State != "removed" {
		t.Fatal("removed preview revived", saved)
	}
	revision, _ := reopened.GetWorkflowRevision(ctx, "revision")
	if revision.State != "cancelled" {
		t.Fatal("queued work survived", revision)
	}
}
func TestPreviewCleanupRejectsUnrelatedHistory(t *testing.T) {
	ctx := context.Background()
	a, data, executor, _ := previewLifetimeFixture(t, "0", nil)
	now := time.Now().UTC()
	if err := data.CreateProject(ctx, core.Project{ID: "other", Name: "other", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, core.App{ID: "foreign", ProjectID: "other", ServerID: "server", Name: "foreign", Generated: true, State: "ready", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "bad-link", RevisionID: "revision", StageName: "old", State: "succeeded", DeploymentResults: []core.WorkflowDeploymentResult{{AppID: "foreign"}}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resource, _ := data.GetWorkflowResource(ctx, "resource")
	if _, err := a.beginWorkflowPreviewCleanup(ctx, resource, "removed", now); err == nil {
		t.Fatal("foreign history accepted as owned")
	}
	if executor.count != 0 {
		t.Fatal("unrelated runtime touched")
	}
	resource, _ = data.GetWorkflowResource(ctx, resource.ID)
	if !resource.Active {
		t.Fatal("invalid ownership partially accepted cleanup")
	}
}
func TestPreviewClosureResolvesReopensAndEveryLinkedPR(t *testing.T) {
	ctx := context.Background()
	a, data, _, _ := previewLifetimeFixture(t, "0", nil)
	resource, _ := data.GetWorkflowResource(ctx, "resource")
	resource.Document += "    ui:\n      repository: example/ui\n"
	if err := data.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	triggers, _ := data.ListWorkflowPreviewTriggers(ctx)
	trigger := triggers[0]
	trigger.LinkedPullRequests = map[string]int{"ui": 9}
	primary, linked := "closed", "open"
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := primary
		if r.URL.Path == "/repos/example/ui/pulls/9" {
			state = linked
		}
		fmt.Fprintf(w, `{"state":%q,"head":{"sha":"abc","ref":"feature"}}`, state)
	}))
	defer github.Close()
	resolver := events.GitHubResolver{BaseURL: github.URL}
	check := func(expected bool) {
		t.Helper()
		open, err := a.workflowPreviewOpenAfterClosure(ctx, trigger, trigger.Repository, resolver)
		if err != nil || open != expected {
			t.Fatalf("open=%v want %v, %v", open, expected, err)
		}
	}
	check(true) // Primary closed while linked UI is open.
	primary, linked = "open", "closed"
	check(true) // Stale primary closed event after reopen.
	primary = "closed"
	check(false)
}

type settlingPreviewExecutor struct{ started, cancelled, release, cleaned chan struct{} }

func (e *settlingPreviewExecutor) Deploy(ctx context.Context, _ core.Deployment, _ core.App, _ core.Server, _ deploy.Progress) error {
	close(e.started)
	<-ctx.Done()
	close(e.cancelled)
	<-e.release
	return ctx.Err()
}
func (e *settlingPreviewExecutor) Cleanup(_ context.Context, _ core.App, _ core.Server, _ deploy.Progress) error {
	select {
	case e.cleaned <- struct{}{}:
	default:
	}
	return nil
}
func TestPreviewCleanupWaitsForCancelledExecutor(t *testing.T) {
	ctx := context.Background()
	_, data, _, _ := previewLifetimeFixture(t, "0", nil)
	executor := &settlingPreviewExecutor{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{}), cleaned: make(chan struct{}, 2)}
	service := deploy.NewService(data, executor)
	a := New(data, service, false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// This test isolates cleanup settlement from the separately tested source gate.
	service.CheckExecution = nil
	if _, err := service.Start(ctx, "current", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executor.started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
	resource, _ := data.GetWorkflowResource(ctx, "resource")
	cleanup, err := a.beginWorkflowPreviewCleanup(ctx, resource, "removed", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- a.reconcileWorkflowPreviewCleanup(ctx, cleanup.ID) }()
	select {
	case <-executor.cancelled:
	case <-time.After(5 * time.Second):
		close(executor.release)
		t.Fatal("running deployment not cancelled")
	}
	select {
	case <-executor.cleaned:
		close(executor.release)
		t.Fatal("cleanup raced unsettled deployment")
	default:
	}
	close(executor.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not resume after executor settled")
	}
}

func TestPreviewCleanupKeepsUnknownRuntimeInspectable(t *testing.T) {
	ctx := context.Background()
	a, data, executor, _ := previewLifetimeFixture(t, "0", nil)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(data.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "node", Name: "node", Driver: "dispatch_agent", Config: map[string]string{}, Details: map[string]string{}, State: "ready", CreatedAt: now, UpdatedAt: now}))
	must(data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "hash", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}))
	must(data.EnrollEdgeCredential(ctx, "node", "hash", "public", "session", now, now.Add(time.Hour)))
	server, err := data.GetServer(ctx, "server")
	must(err)
	server.AgentNodeID = "node"
	must(data.UpdateServer(ctx, server))
	must(data.CreateRuntimeJob(ctx, core.RuntimeJob{ID: "uncertain-deploy", AppID: "current", ProjectID: "project", ServerID: "server", NodeID: "node", NodeGeneration: 1, Operation: "deploy", RequestDigest: "digest", EncryptedRequest: "private", ExpiresAt: now.Add(time.Hour), CreatedAt: now}))
	leased, err := data.LeaseRuntimeJob(ctx, "node", now, time.Minute)
	must(err)
	if leased == nil {
		t.Fatal("job not leased")
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	must(err)
	intent, err := a.beginWorkflowPreviewCleanup(ctx, resource, "removed", now.Add(time.Second))
	must(err)
	if err = a.reconcileWorkflowPreviewCleanup(ctx, intent.ID); err == nil {
		t.Fatal("uncertain effects were hidden")
	}
	if executor.cleaned["current"] {
		t.Fatal("cleanup raced uncertain deployment")
	}
	history, err := data.ListWorkflowPreviewCleanups(ctx, resource.ID)
	must(err)
	if history[0].State != "blocked" || !strings.Contains(history[0].Apps[0].Error, "inspect and acknowledge") {
		t.Fatalf("unknown outcome not inspectable: %+v", history)
	}
	saved, err := data.GetRuntimeJob(ctx, leased.ID)
	must(err)
	if saved.State != "unknown" || saved.EncryptedRequest != "" {
		t.Fatalf("uncertain execution evidence lost: %+v", saved)
	}
	if err = data.CheckWorkflowPreviewCleanup(ctx, "current", "unrelated-operation"); err == nil {
		t.Fatal("direct cleanup bypassed accepted ownership")
	}
}

package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	"github.com/doout/dispatch/internal/store"
)

func TestPreviewAutoRunAllowanceCountsOnlyRecentAutomaticStarts(t *testing.T) {
	now := time.Now().UTC()
	revisions := []core.WorkflowRevision{
		{Trigger: "pull request comment 123", CreatedAt: now.Add(-time.Minute)},
		{Trigger: "pull request update", State: "cancelled", CreatedAt: now.Add(-20 * time.Minute)},
		{Trigger: "pull request update", State: "succeeded", CreatedAt: now.Add(-2 * time.Hour)},
	}
	if !previewAutoRunAllowed(revisions, 2, now) {
		t.Fatal("a manual run or an expired automatic run used the hourly allowance")
	}
	if previewAutoRunAllowed(revisions, 1, now) {
		t.Fatal("a cancelled automatic run did not use the hourly allowance")
	}
	if !previewAutoRunAllowed(revisions, 1, now.Add(41*time.Minute)) {
		t.Fatal("the rolling hourly allowance did not reopen")
	}
}

func TestNewHeadCancelsStaleManualPreviewWithoutDeploying(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "manual-preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldSHA := strings.Repeat("a", 40)
	newSHA := strings.Repeat("b", 40)
	for _, create := range []func() error{
		func() error {
			return data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now})
		},
		func() error {
			return data.CreateSecret(ctx, core.Secret{ID: "credential", Name: "Credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture", CreatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", CredentialSecretID: "credential", Name: "Source", Repository: "example/devops", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "preview", ConfigSourceID: "source", Kind: "Application", Name: "preview", Path: "temporary/preview.yaml", Document: "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview\nspec:\n  sources:\n    service:\n      repository: example/service\n      ref: " + oldSHA + "\n", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "old", ResourceID: "preview", State: "running", Trigger: "pull request comment 1", Sources: map[string]core.WorkflowSourceRevision{"service": {Alias: "service", Repository: "example/service", CommitSHA: oldSHA}}, CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	a := New(data, deploy.NewService(data, deploy.SimulationExecutor{}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	trigger := core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "preview", Repository: "example/service", PullRequestNumber: 42, AutoDeploy: false}
	if err := a.updateWorkflowPreviewHead(ctx, trigger, "example/service", newSHA, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	revisions, err := data.ListWorkflowRevisions(ctx, "preview", 0)
	if err != nil || len(revisions) != 1 || revisions[0].State != "cancelled" {
		t.Fatalf("new head should cancel stale work without an automatic run: %+v, %v", revisions, err)
	}
	trigger.AutoDeploy, trigger.MaxAutoRunsPerHour = true, 1
	if err := a.updateWorkflowPreviewHead(ctx, trigger, "example/service", newSHA, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	revisions, err = data.ListWorkflowRevisions(ctx, "preview", 0)
	if err != nil || len(revisions) != 2 || revisions[0].Trigger != "pull request update" || revisions[0].Sources["service"].CommitSHA != newSHA {
		t.Fatalf("automatic update did not run the newest head: %+v, %v", revisions, err)
	}
	thirdSHA := strings.Repeat("c", 40)
	if err := a.updateWorkflowPreviewHead(ctx, trigger, "example/service", thirdSHA, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	revisions, err = data.ListWorkflowRevisions(ctx, "preview", 0)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("hourly cap did not defer the next automatic run: %+v, %v", revisions, err)
	}
	trigger.AutoDeploy, trigger.LiveReload = false, true
	if err := a.updateWorkflowPreviewHead(ctx, trigger, "example/service", thirdSHA, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	revisions, err = data.ListWorkflowRevisions(ctx, "preview", 0)
	if err != nil || len(revisions) != 3 || revisions[0].Trigger != "pull request update" || revisions[0].Sources["service"].CommitSHA != thirdSHA {
		t.Fatalf("live reload did not bypass disabled automatic deploys and the hourly cap: %+v, %v", revisions, err)
	}
	resource, err := data.GetWorkflowResource(ctx, "preview")
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := pinWorkflowPreviewSources(resource, map[string]string{"service": thirdSHA})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateWorkflowResource(ctx, pinned); err != nil {
		t.Fatal(err)
	}
	manual, err := a.workflows.Start(ctx, "preview", "pull request comment 2")
	if err != nil || manual.Sources["service"].CommitSHA != thirdSHA {
		t.Fatalf("manual command did not bypass the automatic cap: %+v, %v", manual, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		finished, err := data.GetWorkflowRevision(ctx, manual.ID)
		if err != nil {
			t.Fatal(err)
		}
		if finished.State == "succeeded" {
			return
		}
		if finished.State == "failed" || finished.State == "cancelled" {
			t.Fatalf("manual run failed: %+v", finished)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("manual run did not complete")
}

func TestPreviewPollStartsOnceAndClosesWithoutWebhooks(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "poll.db"))
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
			return data.CreateServer(ctx, core.Server{ID: "server", Name: "Test", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: now})
		},
		func() error {
			return data.CreateApp(ctx, core.App{ID: "template", ProjectID: "project", ServerID: "server", Name: "Service", Template: true, BuildType: core.BuildTypeDockerfile, SourceRepo: "https://example.test/acme/service", Domain: "preview-{pr}.example.test", State: "ready", CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err = data.CreateEventTrigger(ctx, core.EventTrigger{ID: "trigger", AppID: "template", Provider: core.EventProviderGitHub, Repository: "acme/service", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Bool
	var failLookup atomic.Bool
	var comments atomic.Int32
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/issues/comments"):
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 501, "body": "/preview", "issue_url": "https://example.test/repos/acme/service/issues/17", "author_association": "MEMBER", "created_at": now.Format(time.RFC3339), "user": map[string]string{"login": "operator"}}})
		case strings.HasSuffix(r.URL.Path, "/pulls/17"):
			if failLookup.Load() {
				http.Error(w, "unavailable", 503)
				return
			}
			state := "open"
			if closed.Load() {
				state = "closed"
			}
			_, _ = fmt.Fprintf(w, `{"state":%q,"head":{"ref":"feature","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"id":101,"full_name":"acme/service"}},"base":{"ref":"main","repo":{"id":101,"full_name":"acme/service"}}}`, state)
		case strings.HasSuffix(r.URL.Path, "/issues/17/comments") && r.Method == http.MethodPost:
			comments.Add(1)
			_, _ = io.WriteString(w, `{"id":900}`)
		case strings.HasSuffix(r.URL.Path, "/issues/comments/900") && r.Method == http.MethodPatch:
			_, _ = io.WriteString(w, `{"id":900}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(github.Close)
	a := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{GitHubAPIURL: github.URL, GitHubToken: "token"})
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	previews, err := data.ListPreviewEnvironments(ctx, "")
	if err != nil || len(previews) != 1 {
		t.Fatalf("expected one preview: %v %#v", err, previews)
	}
	if previews[0].HeadSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || previews[0].SourceCommentID != "501" || comments.Load() != 1 {
		t.Fatalf("unexpected preview identity or status comment: %#v, comments=%d", previews[0], comments.Load())
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	previews, err = data.ListPreviewEnvironments(ctx, "")
	if err != nil || len(previews) != 1 || previews[0].ID == "" {
		t.Fatalf("repeated poll created another preview: %v %#v", err, previews)
	}

	history, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}})
	if err != nil || len(history) != 1 || history[0].Transport != "poll" || history[0].State != "processed" || history[0].PreviewURL == "" {
		t.Fatalf("polled command not recorded once with preview URL: %+v %v", history, err)
	}

	// A signed webhook for the already-polled comment shares both execution and activity identity.
	body := []byte(fmt.Sprintf(`{"action":"created","repository":{"full_name":"acme/service"},"issue":{"number":17,"pull_request":{}},"comment":{"id":501,"body":"/preview","author_association":"MEMBER","user":{"login":"operator"},"created_at":%q}}`, now.Format(time.RFC3339)))
	sign := hmac.New(sha256.New, []byte("webhook-secret"))
	_, _ = sign.Write(body)
	request := httptest.NewRequest("POST", "/events/github", strings.NewReader(string(body)))
	request.Header.Set("X-GitHub-Event", "issue_comment")
	request.Header.Set("X-GitHub-Delivery", "webhook-duplicate")
	request.Header.Set("X-Hub-Signature-256", fmt.Sprintf("sha256=%x", sign.Sum(nil)))
	response := httptest.NewRecorder()
	a.processGitHubWebhook(response, request, "webhook-secret", "", 0, a.groups, a.events)
	if response.Code != 200 {
		t.Fatalf("duplicate webhook: %d %s", response.Code, response.Body.String())
	}
	history, err = data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}})
	if err != nil || len(history) != 1 || history[0].Transport != "poll" {
		t.Fatalf("duplicate webhook changed delivery history: %+v %v", history, err)
	}
	cursor, err := data.PreviewPollCursor(ctx, "", "acme/service")
	if err != nil || cursor == nil {
		t.Fatal("successful scan did not advance its cursor")
	}
	failLookup.Store(true)
	if err := a.PollPreviewsOnce(ctx); err == nil {
		t.Fatal("failed PR lookup was swallowed")
	}
	failedCursor, err := data.PreviewPollCursor(ctx, "", "acme/service")
	if err != nil || !failedCursor.Equal(*cursor) {
		t.Fatal("failed scan advanced its cursor")
	}
	checks, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}, ChecksOnly: true})
	if err != nil || len(checks) != 1 || checks[0].State != "failed" || checks[0].Message == "" {
		t.Fatalf("failed poll not visible: %+v %v", checks, err)
	}
	failLookup.Store(false)
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	history, err = data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}})
	if err != nil || len(history) != 3 {
		t.Fatalf("failure/recovery not recorded: %+v %v", history, err)
	}
	closed.Store(true)
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		previews, err = data.ListPreviewEnvironments(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if previews[0].State == core.PreviewReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	previews, err = data.ListPreviewEnvironments(ctx, "")
	if err != nil || len(previews) != 1 || previews[0].State != core.PreviewClosed {
		t.Fatalf("closed pull request did not clean preview: %v %#v", err, previews)
	}
}

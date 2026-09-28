package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
)

func TestPreviewTestCommentStartsOneCheckRunWithoutCreatingPreview(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "preview-test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	posted := []string{}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/example/service/issues/42/comments" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		posted = append(posted, body.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":101}`)
	}))
	t.Cleanup(github.Close)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}))
	must(data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", APIURL: github.URL, WebURL: github.URL, State: "ready", CreatedAt: now, UpdatedAt: now}))
	must(data.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", GitHubAppID: "github", Name: "Source", Repository: "example/config", Active: true, CreatedAt: now, UpdatedAt: now}))
	document := `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: preview-42
spec:
  sources:
    chart: {repository: example/chart, ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
  deployments:
    app:
      helm: {sourceRef: chart, chartPath: charts/app, releaseName: preview42}
  stages:
    - name: development
      targetRef: dev
      deploy: [app]
      approval: automatic
      url: https://preview.example.test/42
      checks:
        qa: {pipelineRef: missing-qa, when: onDemand, with: {url: "{{ stage.url }}"}}
`
	must(data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "preview", ConfigSourceID: "source", Kind: "Application", Name: "preview-42", Path: "temporary/preview.yaml", Document: document, Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	must(data.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "preview", GitHubAppID: "github", Repository: "example/service", PullRequestNumber: 42, Command: "/preview", PreviewURL: "https://preview.example.test/42", CreatedAt: now}))
	must(data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "deployed", ResourceID: "preview", State: "succeeded", Trigger: "pull request comment 1", Sources: map[string]core.WorkflowSourceRevision{"chart": {Repository: "example/chart", CommitSHA: strings.Repeat("a", 40)}}, CreatedAt: now}))
	must(data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "deployed-stage", RevisionID: "deployed", StageName: "development", State: "succeeded", CreatedAt: now}))
	a := New(data, deploy.NewService(data, deploy.SimulationExecutor{}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{GitHubToken: "token"})
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTriggers: []core.WorkflowPreviewTrigger{{ID: "trigger", ResourceID: "preview", GitHubAppID: "github", Repository: "example/service", PullRequestNumber: 42, Command: "/preview", PreviewURL: "https://preview.example.test/42"}}}
	event := core.IncomingEvent{Kind: core.EventKindPullRequestComment, ProviderConnectionID: "github", Repository: "example/service", Command: "/preview", Arguments: "test", PullRequestNumber: 42, SourceCommentID: "17", TrustedActor: true}
	must(a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}))
	must(a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}))
	revisions, err := data.ListWorkflowRevisions(ctx, "preview", 0)
	must(err)
	if len(revisions) != 2 || !strings.HasPrefix(revisions[0].Trigger, "pull request test ") || len(posted) != 1 || !strings.Contains(posted[0], "checks running") {
		t.Fatalf("test comment created an unexpected run or reply: revisions=%+v comments=%+v", revisions, posted)
	}
	commentID, err := data.WorkflowPreviewTestComment(ctx, revisions[0].ID)
	must(err)
	if commentID != "101" {
		t.Fatalf("test status comment was not saved: %q", commentID)
	}

	bad := event
	bad.Arguments = "test private-value"
	bad.SourceCommentID = "18"
	must(a.consumePolledComment(ctx, target, bad, events.GitHubResolver{}, a.groups, a.events))
	revisions, err = data.ListWorkflowRevisions(ctx, "preview", 0)
	must(err)
	if len(revisions) != 2 {
		t.Fatal("a rejected command started another check run")
	}
	items, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}})
	must(err)
	found := false
	for _, item := range items {
		if item.State == "rejected" {
			found = true
			if item.Message == "" || strings.Contains(item.Message, "private-value") {
				t.Fatalf("unsafe rejection detail: %+v", item)
			}
		}
	}
	if !found {
		t.Fatalf("test rejection disappeared from activity: %+v", items)
	}
}

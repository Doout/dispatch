package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

type previewCleanupRecorder struct{ cleaned map[string]bool }

func (e *previewCleanupRecorder) Deploy(context.Context, core.Deployment, core.App, core.Server, deploy.Progress) error {
	return nil
}

func (e *previewCleanupRecorder) Cleanup(_ context.Context, app core.App, _ core.Server, _ deploy.Progress) error {
	e.cleaned[app.ID] = true
	return nil
}

func TestClosedWorkflowPreviewCleansCurrentAndOlderHelmApps(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "preview-close.db"))
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
			return data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: "project", GitHubAppID: "github", Name: "Config", Repository: "Example/devops", Branch: "main", Path: ".dispatch", SyncMode: core.ConfigSyncPoll, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "config", APIVersion: "dispatch/v1alpha1", Kind: "Application", Name: "preview-42", Path: "temporary.yaml", Document: "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview-42\nspec:\n  sources:\n    service:\n      repository: Example/service\n    ui:\n      repository: Example/ui\n", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "resource", GitHubAppID: "github", Repository: "Example/service", PullRequestNumber: 42, Command: "/preview", LinkedPullRequests: map[string]int{"ui": 123}, CreatedAt: now})
		},
		func() error {
			return data.CreateApp(ctx, core.App{ID: "current", ProjectID: "project", ServerID: "server", Name: "Current", BuildType: core.BuildTypeHelm, State: "ready", Generated: true, HelmProvenance: core.HelmProvenance{WorkflowResourceID: "resource", PullRequests: []core.HelmPullRequest{{Repository: "Example/service", Number: 42}}}, CreatedAt: now})
		},
		func() error {
			return data.CreateApp(ctx, core.App{ID: "older", ProjectID: "project", ServerID: "server", Name: "Older", BuildType: core.BuildTypeHelm, State: "ready", Generated: true, CreatedAt: now})
		},
		func() error {
			return data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "revision", ResourceID: "resource", State: "succeeded", CreatedAt: now})
		},
		func() error {
			return data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "stage", RevisionID: "revision", StageName: "dev", State: "succeeded", DeploymentResults: []core.WorkflowDeploymentResult{{AppID: "older"}}, CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	executor := &previewCleanupRecorder{cleaned: map[string]bool{}}
	a := New(data, deploy.NewService(data, executor), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if triggers[0].LinkedPullRequests["ui"] != 123 {
		t.Fatalf("linked UI PR was not persisted: %+v", triggers[0])
	}
	resources := []core.WorkflowResource{{ID: "resource", Temporary: true, Path: "temporary.yaml", Document: "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview-42\nspec:\n  sources:\n    service:\n      repository: Example/service\n    ui:\n      repository: Example/ui\n"}}
	if err := a.enrichWorkflowPreviewPullRequests(ctx, resources); err != nil {
		t.Fatal(err)
	}
	links := resources[0].PreviewPullRequests
	if len(links) != 2 || links[0].URL != "https://github.example.com/example/service/pull/42" || links[1].URL != "https://github.example.com/example/ui/pull/123" {
		t.Fatalf("preview PR links are incorrect: %+v", links)
	}
	if err := a.closeWorkflowPreview(ctx, triggers[0]); err != nil {
		t.Fatal(err)
	}
	if !executor.cleaned["current"] || !executor.cleaned["older"] {
		t.Fatalf("preview Helm apps not cleaned: %+v", executor.cleaned)
	}
	for _, id := range []string{"current", "older"} {
		app, err := data.GetApp(ctx, id)
		if err != nil || app.State != "closed" {
			t.Fatalf("app %s not closed: %+v, %v", id, app, err)
		}
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.Active {
		t.Fatalf("preview workflow still active: %+v, %v", resource, err)
	}
	triggers, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || triggers[0].ClosedAt == nil {
		t.Fatalf("preview trigger not closed: %+v, %v", triggers, err)
	}
	if err := data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "manual-resource", ConfigSourceID: "config", APIVersion: "dispatch/v1alpha1", Kind: "Application", Name: "manual-preview", Path: "temporary/manual.yaml", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "manual-trigger", ResourceID: "manual-resource", GitHubAppID: "github", Repository: "Example/service", PullRequestNumber: 43, Command: "/preview", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, core.App{ID: "manual-app", ProjectID: "project", ServerID: "server", Name: "Manual", BuildType: core.BuildTypeHelm, State: "ready", Generated: true, HelmProvenance: core.HelmProvenance{WorkflowResourceID: "manual-resource"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "manual-resource")
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/workflow/temporary-resources/manual-resource", nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	pending := core.WorkflowRevision{ID: "manual-revision", ResourceID: "manual-resource", State: "queued", CreatedAt: now}
	if err := data.CreateWorkflowRevision(ctx, pending); err != nil {
		t.Fatal(err)
	}
	blocked := httptest.NewRecorder()
	a.deleteTemporaryWorkflowResource(blocked, request)
	if blocked.Code != http.StatusConflict || executor.cleaned["manual-app"] {
		t.Fatalf("active run deletion returned %d and cleanup %v", blocked.Code, executor.cleaned["manual-app"])
	}
	pending.State = "succeeded"
	if err := data.UpdateWorkflowRevision(ctx, pending); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	a.deleteTemporaryWorkflowResource(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete preview returned %d: %s", response.Code, response.Body.String())
	}
	if !executor.cleaned["manual-app"] {
		t.Fatal("manual delete did not clean the Helm deployment")
	}
	removed, err := data.GetWorkflowResource(ctx, "manual-resource")
	if err != nil || removed.State != "removed" || removed.Active {
		t.Fatalf("manual preview remains visible: %+v, %v", removed, err)
	}
	if _, _, err := a.workflows.Activate(ctx, "manual-resource"); err == nil {
		t.Fatal("deleted PR preview could be reactivated")
	}
	triggers, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || triggers[1].ClosedAt == nil {
		t.Fatalf("manual preview trigger remains active: %+v, %v", triggers, err)
	}
}

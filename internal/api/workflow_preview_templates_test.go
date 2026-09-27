package api

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
)

func TestPreviewTemplateCreatesOneInstancePerPRAndReusesIt(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "templates.db"))
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
			return data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", InstallationID: 1, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: "project", GitHubAppID: "github", Name: "Config", Repository: "Example/devops", Branch: "main", Path: ".dispatch", SyncMode: core.ConfigSyncPoll, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "old", ConfigSourceID: "config", APIVersion: "dispatch/v1alpha1", Kind: "Application", Name: "preview-42", Path: "temporary/old.yaml", Document: "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview-42\nspec:\n  sources:\n    service:\n      repository: Example/service\n      ref: 1111111111111111111111111111111111111111\n", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	template := core.WorkflowPreviewTemplate{ID: "template", ConfigSourceID: "config", GitHubAppID: "github", Name: "Example preview", Repository: "example/service", Command: "/preview", PreviewURL: "https://dev.example.test/app/preview/__PREVIEW_ID__", Document: "apiVersion: dispatch/v1alpha1\nkind: Application\nmetadata:\n  name: preview-__PREVIEW_ID__\nspec:\n  sources:\n    service:\n      repository: Example/service\n      ref: 1111111111111111111111111111111111111111\n", Active: true, CreatedAt: now, UpdatedAt: now}
	if err := data.CreateWorkflowPreviewTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	a := New(data, deploy.NewService(data, &previewCleanupRecorder{cleaned: map[string]bool{}}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	target := &previewPollTarget{connectionID: "github", repository: events.NormalizeRepository(template.Repository), workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	event := core.IncomingEvent{Repository: target.repository, PullRequestNumber: 42, HeadSHA: strings.Repeat("a", 40), Command: "/preview", SourceCommentID: "comment-1", TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	if len(target.workflowTriggers) != 1 {
		t.Fatalf("expected one instance trigger, got %d", len(target.workflowTriggers))
	}
	trigger := target.workflowTriggers[0]
	if trigger.TemplateID != template.ID || trigger.PullRequestNumber != 42 || trigger.PreviewURL == template.PreviewURL || !strings.Contains(trigger.PreviewURL, "/42-") {
		t.Fatalf("template trigger not linked or collision suffix missing: %+v", trigger)
	}
	resource, err := data.GetWorkflowResource(ctx, trigger.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resource.Name, "preview-42-") || !resource.Temporary || !resource.Active {
		t.Fatalf("wrong preview instance: %+v", resource)
	}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	resources, err := data.ListWorkflowResources(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 2 {
		t.Fatalf("duplicate comment created another preview: %+v", resources)
	}
	revisions, err := data.ListWorkflowRevisions(ctx, resource.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 {
		t.Fatalf("duplicate comment created another revision: %+v", revisions)
	}
	if err := data.DeleteWorkflowPreviewTemplate(ctx, template.ID); err != nil {
		t.Fatal(err)
	}
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(triggers) != 1 || triggers[0].ClosedAt != nil {
		t.Fatalf("deleting the template changed its instance trigger: %+v, %v", triggers, err)
	}
}

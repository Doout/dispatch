package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestUpdateWorkflowPreviewTriggerPreservesOrResetsCommentState(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "preview-triggers.db"))
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
			return data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: "project", GitHubAppID: "github", Name: "Slots", Repository: "Example/devops", Branch: "main", Path: "slots", SyncMode: core.ConfigSyncPoll, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "config", Kind: "Application", Name: "preview-42", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "resource", GitHubAppID: "github", Repository: "Example/service", PullRequestNumber: 42, Command: "/preview", PreviewURL: "https://old.example.test", LinkedPullRequests: map[string]int{"ui": 84}, CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.UpdateWorkflowPreviewTriggerComment(ctx, "trigger", "12345"); err != nil {
		t.Fatal(err)
	}
	trigger := core.WorkflowPreviewTrigger{ID: "trigger", GitHubAppID: "github", Repository: "Example/service", PullRequestNumber: 42, Command: "/ship", PreviewURL: "https://new.example.test"}
	if err := data.UpdateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	items, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(items) != 1 || items[0].Command != "/ship" || items[0].PreviewURL != trigger.PreviewURL || items[0].ReportCommentID != "12345" || items[0].LinkedPullRequests["ui"] != 84 {
		t.Fatalf("editing the same PR lost comment state: %+v, %v", items, err)
	}
	trigger.PullRequestNumber = 1500
	if err := data.UpdateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	items, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(items) != 1 || items[0].PullRequestNumber != 1500 || items[0].ReportCommentID != "" || len(items[0].LinkedPullRequests) != 0 {
		t.Fatalf("changing the PR kept old comment state: %+v, %v", items, err)
	}
}

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
			return data.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trigger", ResourceID: "resource", GitHubAppID: "github", Repository: "Example/service", PullRequestNumber: 42, Command: "/preview", PreviewURL: "https://old.example.test", LinkedPullRequests: map[string]int{"ui": 84}, SourceDefaults: map[string]core.WorkflowPreviewSourceDefault{"ui": {Repository: "Example/ui", Branch: "develop"}}, CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.UpdateWorkflowPreviewTriggerComment(ctx, "trigger", "12345"); err != nil {
		t.Fatal(err)
	}
	for _, command := range []struct {
		id      string
		enabled bool
		changed bool
	}{{"123", true, true}, {"123", false, false}, {"122", false, false}, {"124", false, true}} {
		changed, err := data.UpdateWorkflowPreviewLiveReload(ctx, "trigger", command.id, command.enabled)
		if err != nil || changed != command.changed {
			t.Fatalf("live reload command %+v: changed=%t err=%v", command, changed, err)
		}
	}
	trigger := core.WorkflowPreviewTrigger{ID: "trigger", GitHubAppID: "github", Repository: "Example/service", PullRequestNumber: 42, Command: "/ship", AutoDeploy: true, MaxAutoRunsPerHour: 3, PreviewURL: "https://new.example.test"}
	if err := data.UpdateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	items, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(items) != 1 || items[0].Command != "/ship" || !items[0].AutoDeploy || items[0].MaxAutoRunsPerHour != 3 || items[0].PreviewURL != trigger.PreviewURL || items[0].ReportCommentID != "12345" || items[0].LinkedPullRequests["ui"] != 84 || items[0].LiveReloadCommentID != "" {
		t.Fatalf("editing the same PR lost comment state: %+v, %v", items, err)
	}
	if !items[0].LifetimeReportPending {
		t.Fatal("preview settings edit did not schedule a report refresh")
	}
	if items[0].SourceDefaults["ui"].Branch != "develop" {
		t.Fatal("source defaults did not survive persistence and editing")
	}
	trigger.PullRequestNumber = 1500
	if err := data.UpdateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	items, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(items) != 1 || items[0].PullRequestNumber != 1500 || items[0].ReportCommentID != "" || len(items[0].LinkedPullRequests) != 0 || items[0].LiveReloadCommentID != "" {
		t.Fatalf("changing the PR kept old comment state: %+v, %v", items, err)
	}
	if err := data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "auto-run", ResourceID: "resource", State: "succeeded", Trigger: "pull request update", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pending, err := data.PendingWorkflowPreviewReports(ctx, "trigger")
	if err != nil || len(pending) != 1 || pending[0] != "auto-run" {
		t.Fatalf("automatic run was not ready for reporting: %v, %v", pending, err)
	}
	if err := data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "test-run", ResourceID: "resource", State: "failed", Trigger: "pull request test 17", CreatedAt: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	reserved, err := data.ReserveWorkflowPreviewComment(ctx, "trigger", "17")
	if err != nil || !reserved {
		t.Fatalf("test command was not reserved: %v, %v", reserved, err)
	}
	if err := data.CompleteWorkflowPreviewComment(ctx, "trigger", "17", "test-run"); err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateWorkflowPreviewTestComment(ctx, "trigger", "17", "status-comment"); err != nil {
		t.Fatal(err)
	}
	statusComment, err := data.WorkflowPreviewTestComment(ctx, "test-run")
	if err != nil || statusComment != "status-comment" {
		t.Fatalf("test status comment was not retained: %q, %v", statusComment, err)
	}
	pending, err = data.PendingWorkflowPreviewReports(ctx, "trigger")
	if err != nil || len(pending) != 2 || pending[1] != "test-run" {
		t.Fatalf("failed on-demand test was not ready for reporting: %v, %v", pending, err)
	}
	if err := data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "cancelled-test", ResourceID: "resource", State: "cancelled", Trigger: "pull request test 18", CreatedAt: now.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := data.ReserveWorkflowPreviewComment(ctx, "trigger", "18"); err != nil {
		t.Fatal(err)
	}
	if err := data.CompleteWorkflowPreviewComment(ctx, "trigger", "18", "cancelled-test"); err != nil {
		t.Fatal(err)
	}
	pending, err = data.PendingWorkflowPreviewReports(ctx, "trigger")
	if err != nil || len(pending) != 3 || pending[2] != "cancelled-test" {
		t.Fatalf("cancelled on-demand test was not ready for reporting: %v, %v", pending, err)
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil {
		t.Fatal(err)
	}
	resource.Document, resource.SpecDigest = "restored-document", "restored-digest"
	trigger.LinkedPullRequests = map[string]int{"worker": 5}
	trigger.SourceDefaults = map[string]core.WorkflowPreviewSourceDefault{"ui": {Repository: "Example/ui", Branch: "release"}}
	if err := data.SaveWorkflowPreviewSources(ctx, resource, trigger); err != nil {
		t.Fatal(err)
	}
	items, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || items[0].LinkedPullRequests["worker"] != 5 || items[0].SourceDefaults["ui"].Branch != "release" {
		t.Fatalf("source defaults and links did not persist together: %+v, %v", items, err)
	}
	resource.Active = false
	if err := data.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	trigger.LinkedPullRequests = map[string]int{"ui": 99}
	if err := data.SaveWorkflowPreviewSources(ctx, resource, trigger); err == nil {
		t.Fatal("updated sources on an inactive preview")
	}
	items, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || items[0].LinkedPullRequests["worker"] != 5 || items[0].LinkedPullRequests["ui"] != 0 {
		t.Fatalf("failed resource update did not roll back links: %+v, %v", items, err)
	}
}

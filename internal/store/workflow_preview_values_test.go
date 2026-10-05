package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestWorkflowPreviewValuesUpgradePreservesLegacyAndFrozenRevisions(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "legacy-preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	steps := migrationSteps(t, "sqlite")
	through := 0
	for through < len(steps) && !strings.HasPrefix(steps[through].version, "100_") {
		through++
	}
	if err := data.applyMigrations(ctx, steps[:through]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, create := range []func() error{
		func() error {
			return data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now})
		},
		func() error {
			return data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: "project", GitHubAppID: "github", Name: "Config", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "config", Kind: "Application", Name: "preview-42", Temporary: true, Active: true, State: "ready", Document: "saved document", SpecDigest: "saved digest", CreatedAt: now, UpdatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := data.db.ExecContext(ctx, `INSERT INTO workflow_preview_triggers(id,resource_id,github_app_id,repository,pull_request_number,command,created_at) VALUES('trigger','resource','github','owner/app',42,'/preview',?)`, stamp(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := data.db.ExecContext(ctx, `INSERT INTO workflow_revisions(id,resource_id,config_sha,spec_digest,state,trigger_name,sources,outputs,created_at) VALUES('legacy','resource','','saved digest','succeeded','manual','{}','{}',?)`, stamp(now)); err != nil {
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(triggers) != 1 || len(triggers[0].PreviewValues) != 0 {
		t.Fatal("migration invented legacy preview values", triggers, err)
	}
	legacy, err := data.GetWorkflowRevision(ctx, "legacy")
	if err != nil || len(legacy.PreviewValues) != 0 || legacy.SpecDigest != "saved digest" {
		t.Fatal("migration changed legacy revision", legacy, err)
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.Document != "saved document" {
		t.Fatal("migration changed shared document", resource, err)
	}
	values := map[string]map[string]any{"app": {"gateway": "captured", "large": int64(9007199254740993), "unsigned": uint64(18446744073709551615)}}
	trigger := triggers[0]
	trigger.PreviewValues = values
	if err := data.SaveWorkflowPreviewSources(ctx, resource, trigger); err != nil {
		t.Fatal(err)
	}
	triggers, err = data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || triggers[0].PreviewValues["app"]["large"] != int64(9007199254740993) || triggers[0].PreviewValues["app"]["unsigned"] != uint64(18446744073709551615) {
		t.Fatal("trigger persistence rounded literal integers", triggers, err)
	}
	revision := core.WorkflowRevision{ID: "frozen", ResourceID: "resource", State: "queued", SpecDigest: resource.SpecDigest, PreviewValues: values, CreatedAt: now}
	if err := data.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	revision.State = "running"
	revision.PreviewValues["app"]["gateway"] = "later setting"
	if err := data.UpdateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	frozen, err := data.GetWorkflowRevision(ctx, revision.ID)
	if err != nil || frozen.State != "running" || frozen.PreviewValues["app"]["gateway"] != "captured" || frozen.PreviewValues["app"]["large"] != int64(9007199254740993) || frozen.PreviewValues["app"]["unsigned"] != uint64(18446744073709551615) {
		t.Fatal("progress update overwrote frozen values", frozen, err)
	}
}

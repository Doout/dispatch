package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestOverviewStageSummaries(t *testing.T) {
	testOverviewStages(t, filepath.Join(t.TempDir(), "overview.db"))
}
func TestOverviewStageSummariesPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_STORE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	testOverviewStages(t, dsn)
}
func testOverviewStages(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	must(s.Migrate(ctx))
	must(s.SeedDemo(ctx))
	projects, err := s.ListProjects(ctx)
	must(err)
	now := time.Now().UTC()
	must(s.CreateSecret(ctx, core.Secret{ID: "credential", Name: "credential", Type: core.SecretTypeGitHubToken, EnvironmentVariable: "GITHUB_TOKEN", EncryptedValue: "fixture", CreatedAt: now}))
	must(s.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: projects[0].ID, Name: "source", Repository: "example/repository", CredentialSecretID: "credential", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "source", Name: "resource", Kind: "Application", State: "valid", Active: true, CreatedAt: now, UpdatedAt: now}))
	for _, state := range []string{"succeeded", "failed", "cancelled", "queued", "running", "awaiting_approval"} {
		must(s.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: state, ResourceID: "resource", State: "running", CreatedAt: now}))
		must(s.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: state, RevisionID: state, State: state, CreatedAt: now}))
	}
	for _, ids := range [][]string{nil, {"succeeded", "running"}} {
		items, err := s.ListWorkflowStageSummaries(ctx, ids)
		must(err)
		count := 3
		if len(ids) > 0 {
			count = 4
		}
		if len(items) != count {
			t.Fatalf("ids %v: got %d stages", ids, len(items))
		}
		seen := map[string]bool{}
		for _, item := range items {
			if seen[item.ID] || item.State == "failed" || item.State == "cancelled" {
				t.Fatalf("unrelated or duplicate stage %s", item.ID)
			}
			seen[item.ID] = true
		}
	}
	history, err := s.ListWorkflowStageRuns(ctx, "failed")
	must(err)
	if len(history) != 1 || history[0].State != "failed" {
		t.Fatal("detailed history lost")
	}
	if !s.postgres {
		rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT id FROM workflow_stage_runs WHERE state IN ('queued','running','awaiting_approval') OR revision_id IN ('succeeded')`)
		must(err)
		defer rows.Close()
		scan := false
		for rows.Next() {
			var id, parent, unused int
			var detail string
			must(rows.Scan(&id, &parent, &unused, &detail))
			if detail == "SCAN workflow_stage_runs" {
				scan = true
			}
		}
		must(rows.Err())
		if scan {
			t.Fatal("overview scans all historical stages")
		}
	}
}

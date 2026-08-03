package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteMigrationAndDemoSeed(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := data.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if err := data.SeedDemo(ctx); err != nil {
		t.Fatalf("seed must be idempotent: %v", err)
	}

	projects, err := data.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := data.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := data.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deployments, err := data.ListDeployments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || len(servers) != 1 || len(apps) != 3 || len(deployments) != 3 {
		t.Fatalf("unexpected inventory sizes: projects=%d servers=%d apps=%d deployments=%d", len(projects), len(servers), len(apps), len(deployments))
	}
	if deployments[0].App == nil || deployments[0].Server == nil {
		t.Fatal("deployment evidence was not hydrated")
	}
	logs, err := data.ListDeploymentLogs(ctx, deployments[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 evidence log entries, got %d", len(logs))
	}
}

func TestSQLiteNotFound(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetApp(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

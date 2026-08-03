package deploy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/store"
)

func TestSimulationDeploymentCompletesWithEvidence(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
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
	apps, err := data.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appID := ""
	for _, app := range apps {
		active, err := data.ActiveDeploymentForApp(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if active == nil {
			appID = app.ID
			break
		}
	}
	if appID == "" {
		t.Fatal("demo seed did not include an app without active work")
	}

	service := NewService(data, SimulationExecutor{Delay: time.Millisecond})
	created, err := service.Start(ctx, appID, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := data.GetDeployment(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State.Terminal() {
			if current.State != "succeeded" {
				t.Fatalf("deployment ended as %q: %s", current.State, current.Message)
			}
			logs, err := data.ListDeploymentLogs(ctx, created.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(logs) < 7 {
				t.Fatalf("expected full transition evidence, got %d entries", len(logs))
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("deployment did not complete before the deadline")
}

func TestWithinRejectsRepositoryEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := within(root, "../outside"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if path, err := within(root, "Dockerfile"); err != nil || path == "" {
		t.Fatalf("expected a contained path, got %q, %v", path, err)
	}
}

package deploy

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeOperationExcludesDeploymentsAndOtherRepairs(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "operation.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	data.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now})
	data.CreateServer(ctx, core.Server{ID: "s", Name: "s", Runtime: "docker", CreatedAt: now})
	data.CreateApp(ctx, core.App{ID: "app", Name: "app", ProjectID: "p", ServerID: "s", BuildType: core.BuildTypeDockerfile, CreatedAt: now})
	service := NewService(data, SimulationExecutor{})
	if err = service.WithIdleApplication(ctx, "app", func() error {
		if err := service.WithIdleApplication(ctx, "app", func() error { t.Fatal("overlapping repair"); return nil }); !errors.Is(err, ErrDeploymentActive) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = data.CreateDeployment(ctx, core.Deployment{ID: "queued", AppID: "app", State: core.DeploymentQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = service.WithIdleApplication(ctx, "app", func() error { t.Fatal("repair overlapped accepted deployment"); return nil }); !errors.Is(err, ErrDeploymentActive) {
		t.Fatal(err)
	}
}

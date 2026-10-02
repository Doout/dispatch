package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"path/filepath"
	"testing"
	"time"
)

func TestHealthPolicyCaptureSurvivesApplicationEdit(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "health-project", Name: "Health", CreatedAt: now}
	if err = data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	server := core.Server{ID: "health-server", Name: "Health", Runtime: "docker", Address: "local", State: "ready", CreatedAt: now}
	if err = data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	policy, _ := core.NormalizeHealthPolicy(core.HealthPolicy{TimeoutSeconds: 15, Checks: []core.HealthCheck{{ID: "http", Kind: "http", Path: "/ready"}}})
	app := core.App{ID: "health-app", ProjectID: project.ID, ServerID: server.ID, Name: "Health", BuildType: core.BuildTypeDockerfile, HealthPolicy: policy, CreatedAt: now}
	if err = data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	before := app.SpecDigest()
	deployment := core.Deployment{ID: "health-deployment", AppID: app.ID, State: core.DeploymentQueued, CreatedAt: now, Health: core.DeploymentHealth{Policy: policy, State: "pending"}}
	if err = data.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	app.HealthPolicy.TimeoutSeconds = 60
	if err = data.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if app.SpecDigest() == before {
		t.Fatal("health policy omitted from accepted spec digest")
	}
	deployment.Health.State = "failed"
	deployment.Health.Checks = []core.HealthCheckResult{{Check: policy.Checks[1], State: "failed", HTTPStatus: 503}}
	if err = data.UpdateDeploymentHealth(ctx, deployment.ID, deployment.Health); err != nil {
		t.Fatal(err)
	}
	got, err := data.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Health.Policy.TimeoutSeconds != 15 || got.Health.Checks[0].HTTPStatus != 503 || got.App.HealthPolicy.TimeoutSeconds != 60 {
		t.Fatalf("historical policy changed: %+v", got.Health)
	}
	summaries, err := data.ListDeploymentSummaries(ctx, 10)
	if err != nil || len(summaries) != 1 || summaries[0].Health.State != "failed" {
		t.Fatalf("health missing from list %v", err)
	}
}

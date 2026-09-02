package api

import (
	"context"
	"encoding/json"
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
)

func TestServerTopologyIncludesGeneratedWorkflowRelease(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	project := core.Project{ID: "project", Name: "Checkout", CreatedAt: now}
	target := core.Server{ID: "target", Name: "cluster-1", Runtime: core.ServerRuntimeKubernetes, State: "ready", Kubernetes: &core.KubernetesServerConfig{Namespace: "apps"}, CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}

	apps := []core.App{
		{ID: "preview", ProjectID: project.ID, ServerID: target.ID, Name: "Preview", BuildType: core.BuildTypeHelm, HelmChart: "helm/preview", HelmRelease: "preview-1", State: "ready", CreatedAt: now},
		{ID: "slot", ProjectID: project.ID, ServerID: target.ID, Name: "Managed slot", BuildType: core.BuildTypeHelm, HelmChart: "helm/slot", HelmRelease: "slot-1", Generated: true, State: "ready", CreatedAt: now.Add(time.Minute)},
	}
	for _, app := range apps {
		if err := data.CreateApp(ctx, app); err != nil {
			t.Fatal(err)
		}
		deployment := core.Deployment{ID: "deployment-" + app.ID, AppID: app.ID, CommitSHA: "0123456789abcdef", State: core.DeploymentSucceeded, CreatedAt: now.Add(2 * time.Minute), Snapshot: core.DeploymentSnapshot{TargetID: target.ID, TargetName: target.Name, Namespace: "apps", Release: app.HelmRelease, Chart: app.HelmChart}}
		if err := data.CreateDeployment(ctx, deployment); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), false, AuthConfig{AdminToken: "secret"}, logger)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/servers/"+target.ID+"/topology", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("server topology returned %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("server topology must not be cached: %q", response.Header().Get("Cache-Control"))
	}

	var topology runtimeTopology
	if err := json.NewDecoder(response.Body).Decode(&topology); err != nil {
		t.Fatal(err)
	}
	releases := map[string]bool{}
	for _, node := range topology.Nodes {
		if node.Kind == "release" {
			releases[node.Label] = true
		}
	}
	if !releases["preview-1"] || !releases["slot-1"] || len(releases) != 2 {
		t.Fatalf("expected saved and managed releases, got %#v", releases)
	}
}

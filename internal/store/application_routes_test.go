package store

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"path/filepath"
	"testing"
	"time"
)

func TestApplicationRouteOwnershipAndProbeRace(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "routes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(data.Migrate(ctx))
	now := time.Now().UTC()
	must(data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}))
	server := core.Server{ID: "target", Name: "Target", Runtime: "docker", Address: "local", State: "ready", Routing: &core.RoutingConfig{BaseDomain: "apps.example.com", RequireTLS: true, TLSResolver: "acme"}, CreatedAt: now}
	must(data.CreateServer(ctx, server))
	loaded, err := data.GetServer(ctx, server.ID)
	must(err)
	if loaded.Routing == nil || loaded.Routing.BaseDomain != server.Routing.BaseDomain {
		t.Fatal("routing configuration was not persisted")
	}
	for _, id := range []string{"app", "other"} {
		must(data.CreateApp(ctx, core.App{ID: id, ProjectID: "project", ServerID: "target", Name: id, BuildType: core.BuildTypeDockerfile, CreatedAt: now}))
	}
	first := core.ApplicationRoute{AppID: "app", ProjectID: "project", ServerID: "target", Hostname: "checkout.apps.example.com", RequestedDeploymentID: "first", State: "reserved", UpdatedAt: now}
	_, err = data.ReserveApplicationRoute(ctx, first)
	must(err)
	competitor := first
	competitor.AppID = "other"
	if _, err = data.ReserveApplicationRoute(ctx, competitor); !errors.Is(err, ErrRouteConflict) {
		t.Fatal("hostname was stolen", err)
	}
	first.DeploymentID, first.Destination, first.State = "first", "http://127.0.0.1:32001", "published"
	must(data.SaveApplicationRoute(ctx, first))
	oldProbe, err := data.GetApplicationRoute(ctx, "app")
	must(err)
	second := first
	second.RequestedDeploymentID = "second"
	_, err = data.ReserveApplicationRoute(ctx, second)
	must(err)
	second.DeploymentID, second.Destination, second.State = "second", "http://127.0.0.1:32002", "published"
	second.PreviousDeploymentID, second.PreviousDestination = first.DeploymentID, first.Destination
	must(data.SaveApplicationRoute(ctx, second))
	oldResult := oldProbe
	oldResult.State = "active"
	if err = data.SaveApplicationRouteObservation(ctx, oldProbe, oldResult); !errors.Is(err, ErrRouteConflict) {
		t.Fatal("stale public probe overwrote promoted route", err)
	}
	current, err := data.GetApplicationRoute(ctx, "app")
	must(err)
	if current.DeploymentID != "second" || current.PreviousDeploymentID != "first" {
		t.Fatal("active route evidence lost", current)
	}
	verified := current
	verified.State = "active"
	verified.Certificate.State = "verified"
	must(data.SaveApplicationRouteObservation(ctx, current, verified))
	must(data.SaveApplicationRoute(ctx, second))
	current, err = data.GetApplicationRoute(ctx, "app")
	must(err)
	if current.State != "active" {
		t.Fatal("replayed receipt erased newer verification")
	}
	stale := current
	stale.DeploymentID = "first"
	if err = data.SaveApplicationRoute(ctx, stale); !errors.Is(err, ErrRouteConflict) {
		t.Fatal("delayed preparation replaced promoted destination", err)
	}
}

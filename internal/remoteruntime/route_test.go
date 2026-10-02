package remoteruntime

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/runtimecontract"
	"testing"
)

func TestRouteEvidenceRejectsOwnershipDestinationAndPublicReadinessClaims(t *testing.T) {
	r := NewRequest(runtimecontract.Deploy, core.Deployment{ID: "deployment"}, core.App{ID: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080}, core.Server{ID: "server", Routing: &core.RoutingConfig{BaseDomain: "apps.example.com"}})
	plan, err := routing.Plan(r.Deployment, r.Application, r.Server)
	if err != nil {
		t.Fatal(err)
	}
	plan.State = "preparing"
	if err = r.ValidateRoute(plan); err != nil {
		t.Fatal(err)
	}
	plan.State = "published"
	plan.DeploymentID = "deployment"
	plan.Destination = "http://127.0.0.1:32000"
	if err = r.ValidateRoute(plan); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*core.ApplicationRoute){
		func(v *core.ApplicationRoute) { v.AppID = "other" }, func(v *core.ApplicationRoute) { v.ProjectID = "other" }, func(v *core.ApplicationRoute) { v.ServerID = "other" }, func(v *core.ApplicationRoute) { v.Hostname = "other.example.com" }, func(v *core.ApplicationRoute) { v.DeploymentID = "other" }, func(v *core.ApplicationRoute) { v.RequestedDeploymentID = "other" }, func(v *core.ApplicationRoute) { v.Destination = "http://169.254.169.254:8080" }, func(v *core.ApplicationRoute) { v.State = "active" }, func(v *core.ApplicationRoute) { v.RequireTLS = true },
	} {
		copy := *plan
		change(&copy)
		if r.ValidateRoute(&copy) == nil {
			t.Fatal("invalid runtime evidence accepted", copy)
		}
	}
}

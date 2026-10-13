package remoteruntime

import (
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/runtimecontract"
	"strings"
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

func TestHostedPreviewRoutingSurvivesWorkerWireFormat(t *testing.T) {
	scope := &core.HostedRoutingScope{TenantID: "tenant", TenantSlug: "agentops", RootDomain: "doodship.com"}
	app := core.App{ID: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeDockerfile, ContainerPort: 8080, Generated: true, HelmProvenance: core.HelmProvenance{PullRequests: []core.HelmPullRequest{{Number: 223}}}}
	server := core.Server{ID: "server", Routing: &core.RoutingConfig{BaseDomain: scope.RootDomain, Hosted: scope, RequireTLS: true}}
	r := NewRequest(runtimecontract.Deploy, core.Deployment{ID: "deployment"}, app, server)
	before, err := routing.Plan(r.Deployment, app, server)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var wire Request
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	after, err := routing.Plan(wire.Deployment, wire.App(), wire.Server)
	if err != nil || before.Hostname != after.Hostname || !strings.HasPrefix(after.Hostname, "p223-") || !strings.HasSuffix(after.Hostname, "-agentops.doodship.com") {
		t.Fatal(before, after, err)
	}
	after.State = "published"
	after.DeploymentID = "deployment"
	after.Destination = "http://127.0.0.1:32000"
	if err := wire.ValidateRoute(after); err != nil {
		t.Fatal(err)
	}
	after.GatewayTLS = false
	if err := wire.ValidateRoute(after); err == nil {
		t.Fatal("worker changed the accepted TLS boundary")
	}
}

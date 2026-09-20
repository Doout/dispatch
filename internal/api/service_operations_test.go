package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestServiceImpactRecognizesUnchangedBoundApplicationAfterCredentialRotation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.store.CreateServer(ctx, core.Server{ID: "impact-server", Name: "impact-server", Runtime: core.ServerRuntimeDocker, Address: "local", State: "ready", CreatedAt: now}))
	app := core.App{ID: "impact-app", Name: "impact-app", ProjectID: projects[0].ID, ServerID: "impact-server", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	must(a.store.CreateApp(ctx, app))
	dependency := core.Service{ID: "impact-service", Name: "dependency", ProjectID: app.ProjectID, Type: "generic", Fields: map[string]core.ServiceField{"url": {Value: "https://before.example", Configured: true}}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateService(ctx, dependency))
	bindings := []core.ServiceBinding{{Alias: "dependency", ServiceRef: dependency.ID, Environment: map[string]string{"DEPENDENCY_URL": "url"}}}
	must(a.store.ReplaceAppServiceBindings(ctx, app.ID, bindings))
	d := core.Deployment{ID: "impact-deployment", AppID: app.ID, CommitSHA: "pinned-source", SpecDigest: app.SpecDigest(), State: core.DeploymentSucceeded, CreatedAt: now}
	must(a.store.CreateDeployment(ctx, d))
	check := func(want bool) {
		t.Helper()
		raw := serviceRequestTest(t, a, "GET", "/api/v1/services/"+dependency.ID+"/impact", nil, 200)
		var out struct {
			Consumers []serviceImpactConsumer `json:"consumers"`
		}
		must(json.Unmarshal(raw, &out))
		if len(out.Consumers) != 1 || out.Consumers[0].CanRedeploy != want {
			t.Fatalf("consumer eligible=%v, want %v", out.Consumers, want)
		}
	}
	check(true)
	status, err := a.applicationSync(ctx, app.ID)
	must(err)
	if status.Revision.State != "current" {
		t.Fatal("unchanged bindings shown as needing redeploy", status.Revision.State)
	}
	dependency.Revision = 2
	dependency.Fields["url"] = core.ServiceField{Value: "https://rotated.example", Configured: true}
	must(a.store.UpdateService(ctx, dependency, 1))
	check(true)
	bindings[0].Environment = map[string]string{"CHANGED_MAPPING": "url"}
	must(a.store.ReplaceAppServiceBindings(ctx, app.ID, bindings))
	check(false)
}

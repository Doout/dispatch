package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
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

func TestServiceConsumerRedeployRedactsOperatorCredentials(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	app, dependency, _ := serviceConsumerFixture(t, a, core.BuildTypeDockerfile)
	now := time.Now().UTC()
	user := core.User{ID: "consumer-operator", Username: "consumer-operator", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "consumer-role", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: app.ProjectID, Role: "operator", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"revision": dependency.Revision, "appIds": []string{app.ID}, "confirm": true})
	req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", dependency.ID)
	req = req.WithContext(context.WithValue(withIdentity(req.Context(), core.Identity{ID: user.ID, SystemRole: "member"}), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	a.redeployServiceConsumers(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("redeploy status %d: %s", response.Code, response.Body.String())
	}
	var results []struct {
		Deployment *core.Deployment `json:"deployment"`
		Error      string           `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Deployment == nil || results[0].Deployment.App == nil || results[0].Error != "" {
		t.Fatal("consumer deployment was not accepted")
	}
	d := results[0].Deployment
	if d.App.SourceCredentialID != "" || len(d.App.HookSecretIDs) != 0 || strings.Contains(response.Body.String(), "consumer-source-credential") || strings.Contains(response.Body.String(), "consumer-hook-credential") {
		t.Fatal("operator response exposed global credential references")
	}
	stored, err := a.store.GetApp(ctx, app.ID)
	if err != nil || stored.SourceCredentialID != app.SourceCredentialID || len(stored.HookSecretIDs) != 1 {
		t.Fatal("response redaction changed saved application credentials")
	}
	// Let the local simulation finish before closing its database.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, err := a.store.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.FinishedAt != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("simulation did not finish")
}

func TestServiceImpactRejectsChangedImplicitHelmRelease(t *testing.T) {
	a := serviceTestAPI(t)
	app, dependency, _ := serviceConsumerFixture(t, a, core.BuildTypeHelm)
	app.Name = "renamed-consumer"
	if err := a.store.UpdateApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/services/"+dependency.ID+"/impact", nil, 200)
	var result struct {
		Consumers []serviceImpactConsumer `json:"consumers"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 1 || result.Consumers[0].CanRedeploy {
		t.Fatal("renamed Helm release remained eligible for bulk redeploy")
	}
	serviceRequestTest(t, a, "POST", "/api/v1/services/"+dependency.ID+"/redeploy", map[string]any{"revision": dependency.Revision, "appIds": []string{app.ID}, "confirm": true}, 409)
	active, err := a.store.ActiveDeploymentForApp(context.Background(), app.ID)
	if err != nil || active != nil {
		t.Fatal("unreviewed release rename started a deployment")
	}
}

func serviceConsumerFixture(t *testing.T, a *API, build core.BuildType) (core.App, core.Service, core.Deployment) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	projects, err := a.store.ListProjects(ctx)
	must(err)
	server := core.Server{ID: "consumer-server", Name: "consumer-server", Runtime: core.ServerRuntimeDocker, Address: "local", State: "ready", CreatedAt: now}
	if build == core.BuildTypeHelm {
		server.Runtime = core.ServerRuntimeKubernetes
		server.Kubernetes = &core.KubernetesServerConfig{Namespace: "default"}
	}
	must(a.store.CreateServer(ctx, server))
	app := core.App{ID: "consumer-app", Name: "consumer-app", ProjectID: projects[0].ID, ServerID: server.ID, BuildType: build, SourceCredentialID: "consumer-source-credential", HookEnvironment: map[string]string{core.SecretEnvironmentKey("consumer-hook-credential", "HOOK_TOKEN"): "encrypted-hook-value"}, CreatedAt: now}
	must(a.store.CreateApp(ctx, app))
	app, err = a.store.GetApp(ctx, app.ID)
	must(err)
	dependency := core.Service{ID: "consumer-service", Name: "dependency", ProjectID: app.ProjectID, Type: "generic", Fields: map[string]core.ServiceField{"url": {Value: "https://dependency.example", Configured: true}}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateService(ctx, dependency))
	binding := core.ServiceBinding{Alias: "dependency", ServiceRef: dependency.ID, Environment: map[string]string{"DEPENDENCY_URL": "url"}}
	if build == core.BuildTypeHelm {
		binding.Environment = nil
		binding.Helm = &core.ServiceHelmBinding{Keys: map[string]string{"endpoint": "url"}, SecretNameValues: []string{"dependency.existingSecret"}}
	}
	must(a.store.ReplaceAppServiceBindings(ctx, app.ID, []core.ServiceBinding{binding}))
	d := core.Deployment{ID: "consumer-deployment", AppID: app.ID, CommitSHA: "pinned-source", SpecDigest: app.SpecDigest(), State: core.DeploymentSucceeded, CreatedAt: now}
	if build == core.BuildTypeHelm {
		d.Snapshot.Release = app.Name
	}
	must(a.store.CreateDeployment(ctx, d))
	return app, dependency, d
}

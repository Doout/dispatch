package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
)

func applicationUpdateFixture(t *testing.T) (*API, core.App) {
	t.Helper()
	a := serviceTestAPI(t)
	projects, err := a.store.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := core.Server{ID: "saved-app-target", Name: "Saved app target", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/apps", map[string]any{"projectId": projects[0].ID, "serverId": server.ID, "name": "Saved application", "sourceRepo": "https://example.test/service.git", "containerPort": 8080, "domain": "service.example.test"}, 201)
	var app core.App
	if err := json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	return a, app
}

func TestApplicationUpdatePreservesIdentityHistoryAndExplicitClears(t *testing.T) {
	a, app := applicationUpdateFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d := core.Deployment{ID: "retained-original", AppID: app.ID, CommitSHA: "original-source", SpecDigest: app.SpecDigest(), State: core.DeploymentSucceeded, CreatedAt: now, FinishedAt: &now}
	if err := a.store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/apps/" + app.ID
	var before core.ApplicationConfiguration
	if err := json.Unmarshal(serviceRequestTest(t, a, "GET", path, nil, 200), &before); err != nil {
		t.Fatal(err)
	}
	if before.SpecDigest != app.SpecDigest() {
		t.Fatal("GET omitted the current saved specification", before)
	}
	patch := map[string]any{"expectedSpecDigest": before.SpecDigest, "branch": "release", "contextPath": "backend", "domain": "", "containerPort": 0}
	var after core.ApplicationConfiguration
	if err := json.Unmarshal(serviceRequestTest(t, a, "PATCH", path, patch, 200), &after); err != nil {
		t.Fatal(err)
	}
	if after.ID != app.ID || after.Name != app.Name || after.ProjectID != app.ProjectID || after.ServerID != app.ServerID || after.State != app.State || !after.CreatedAt.Equal(app.CreatedAt) || after.Branch != "release" || after.ContextPath != "backend" || after.Domain != "" || after.ContainerPort != 0 || after.SpecDigest == before.SpecDigest {
		t.Fatal("partial edit lost application identity or explicit clears", after)
	}
	history, err := a.store.ListDeployments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, attempt := range history {
		if attempt.AppID == app.ID {
			count++
			if attempt.ID != d.ID || attempt.SpecDigest != d.SpecDigest {
				t.Fatal("saved edit changed history", attempt)
			}
		}
	}
	if count != 1 {
		t.Fatal("editing saved configuration started a deployment", count)
	}
	serviceRequestTest(t, a, "PATCH", path, patch, 409)
	compose := "services:\n  web:\n    image: example/web:1\n"
	response := serviceRequestTest(t, a, "PATCH", path, map[string]any{"expectedSpecDigest": after.SpecDigest, "buildType": "compose", "composeContent": compose}, 200)
	if strings.Contains(string(response), "example/web:1") {
		t.Fatal("configuration response exposed private inline content")
	}
	saved, err := a.store.GetApp(ctx, app.ID)
	if err != nil || saved.ComposeContent != strings.TrimSpace(compose) || saved.Branch != "" || saved.BuildType != core.BuildTypeCompose {
		t.Fatal(saved, err)
	}
}

func TestApplicationUpdateRejectsInvalidConfigurationAndIdentityFields(t *testing.T) {
	a, app := applicationUpdateFixture(t)
	for _, input := range []map[string]any{
		{"branch": "new"}, {"expectedSpecDigest": app.SpecDigest()},
		{"expectedSpecDigest": app.SpecDigest(), "serverId": "other-target"},
		{"expectedSpecDigest": app.SpecDigest(), "name": "renamed"},
		{"expectedSpecDigest": app.SpecDigest(), "containerPort": 65536},
		{"expectedSpecDigest": app.SpecDigest(), "domain": "https://example.test"},
		{"expectedSpecDigest": app.SpecDigest(), "contextPath": "../outside"},
		{"expectedSpecDigest": app.SpecDigest(), "sourceRepo": "https://user:private@example.test/repo.git"},
		{"expectedSpecDigest": app.SpecDigest(), "buildType": "unknown"},
		{"expectedSpecDigest": app.SpecDigest(), "buildType": "helm", "helmChart": "oci://example.test/chart"},
		{"expectedSpecDigest": app.SpecDigest(), "buildType": "compose", "composeContent": "services: [invalid"},
	} {
		want := 422
		if input["serverId"] != nil || input["name"] != nil {
			want = 400
		}
		serviceRequestTest(t, a, "PATCH", "/api/v1/apps/"+app.ID, input, want)
	}
	app.HealthPolicy = core.HealthPolicy{Checks: []core.HealthCheck{{ID: "route", Kind: "http", Scope: "route", Path: "/"}}}
	if err := a.store.UpdateApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PATCH", "/api/v1/apps/"+app.ID, map[string]any{"expectedSpecDigest": app.SpecDigest(), "domain": ""}, 422)
}

func TestApplicationUpdateScopedGrantsAndCredentials(t *testing.T) {
	a, app := applicationUpdateFixture(t)
	token, grant := scopedServiceIdentity(t, a, app.ProjectID, core.PermissionProjectView, core.PermissionProjectConfigure)
	path := "/api/v1/apps/" + app.ID
	patch := map[string]any{"expectedSpecDigest": app.SpecDigest(), "branch": "scoped-release"}
	response := automationRequest(t, a, token, "PATCH", path, patch, 200)
	var current core.ApplicationConfiguration
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	patch["expectedSpecDigest"] = current.SpecDigest
	patch["sourceCredentialId"] = "global-reference"
	automationRequest(t, a, token, "PATCH", path, patch, 403)
	delete(patch, "sourceCredentialId")
	grant.Permissions = []core.Permission{core.PermissionProjectView}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	automationRequest(t, a, token, "PATCH", path, patch, 403)
	credential := core.Secret{ID: "saved-repository-key", Name: "Repository key", Type: core.SecretTypeGitHubToken, EncryptedValue: "encrypted-value", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateSecret(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PATCH", path, map[string]any{"expectedSpecDigest": current.SpecDigest, "sourceAuthType": "github_token", "sourceCredentialId": credential.ID}, 200)
	saved, err := a.store.GetApp(context.Background(), app.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	response = automationRequest(t, a, token, "GET", path, nil, 200)
	if strings.Contains(response.Body.String(), credential.ID) || strings.Contains(response.Body.String(), credential.EncryptedValue) {
		t.Fatal("scoped GET exposed repository credentials")
	}
	patch = map[string]any{"expectedSpecDigest": saved.SpecDigest(), "branch": "credential-preserved"}
	automationRequest(t, a, token, "PATCH", path, patch, 200)
	saved, _ = a.store.GetApp(context.Background(), app.ID)
	if saved.SourceCredentialID != credential.ID {
		t.Fatal("scoped edit discarded the owner's source credential")
	}
	automationRequest(t, a, token, "PATCH", path, map[string]any{"expectedSpecDigest": saved.SpecDigest(), "sourceRepo": "https://another.example.test/repo.git"}, 403)
}

func TestApplicationConfigurationDiscoversBlockingRuntimeAcrossOperators(t *testing.T) {
	a, node, session, app := runtimeAPIFixture(t)
	token, _ := scopedServiceIdentity(t, a, app.ProjectID, core.PermissionProjectView, core.PermissionProjectConfigure)
	job := submitRuntimeTest(t, a, app, "stop", "original-operator-stop", 202)
	path := "/api/v1/apps/" + app.ID
	check := func(state string) {
		t.Helper()
		reply := automationRequest(t, a, token, "GET", path, nil, 200)
		var out core.ApplicationConfiguration
		if err := json.Unmarshal(reply.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.BlockingRuntimeJob == nil || out.BlockingRuntimeJob.ID != job.ID || out.BlockingRuntimeJob.State != state || out.BlockingRuntimeJob.Location != path+"/runtime/jobs/"+job.ID {
			t.Fatal("another operator could not discover the blocking job", reply.Body.String())
		}
		for _, private := range []string{"encryptedRequest", "encryptedResult", "leaseToken", "requestDigest", "original-operator-stop"} {
			if strings.Contains(reply.Body.String(), private) {
				t.Fatal("discovery exposed private execution data", private)
			}
		}
		automationRequest(t, a, token, "GET", out.BlockingRuntimeJob.Location, nil, 200)
	}
	check("pending")
	var leased remoteruntime.LeasedJob
	if err := json.Unmarshal(runtimeNodeRequest(t, a, "GET", "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/next", session.Token, nil, http.StatusOK), &leased); err != nil {
		t.Fatal(err)
	}
	check("running")
	if err := a.runtimeBroker().Store.ExpireRuntimeJobs(context.Background(), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check("unknown")
	automationRequest(t, a, token, "PATCH", path, map[string]any{"expectedSpecDigest": app.SpecDigest(), "branch": "blocked"}, 409)
}

func TestApplicationUpdateRejectsSourceManagedConfiguration(t *testing.T) {
	a, app := applicationUpdateFixture(t)
	now := time.Now().UTC()
	if err := a.store.CreateSecret(context.Background(), core.Secret{ID: "managed-credential", Name: "Config credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "encrypted-fixture", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateConfigSource(context.Background(), core.ConfigSource{ID: "managed-config", ProjectID: app.ProjectID, CredentialSecretID: "managed-credential", Name: "Managed config", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateWorkflowResource(context.Background(), core.WorkflowResource{ID: "managed-source", ConfigSourceID: "managed-config", Kind: "Application", Name: "Managed source", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	app.HelmProvenance.WorkflowResourceID = "managed-source"
	if err := a.store.UpdateApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PATCH", "/api/v1/apps/"+app.ID, map[string]any{"expectedSpecDigest": app.SpecDigest(), "branch": "manual"}, 409)
}

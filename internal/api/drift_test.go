package api

import (
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

func TestApplicationSyncStatusPermissionsAndMissingBaseline(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0]
	now := time.Now().UTC()
	server := core.Server{ID: "drift-api-server", Name: "drift-api-server", Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{Namespace: "default"}, CreatedAt: now}
	if err := a.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "drift-api-app", Name: "drift-api", ProjectID: project.ID, ServerID: server.ID, BuildType: core.BuildTypeHelm, CreatedAt: now}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	d := core.Deployment{ID: "drift-api-deployment", AppID: app.ID, State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CommitSHA: "original", CreatedAt: now}
	if err := a.store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/sync", nil, 200)
	var status applicationSync
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if status.Drift.State != "unknown" || status.ReapplyAvailable || status.Revision.State != "current" {
		t.Fatal(status)
	}
	// A missing comparison baseline must not erase a completed health observation.
	for _, health := range []string{"healthy", "unknown"} {
		check := core.DriftCheck{DeploymentID: d.ID, State: "unknown", Health: health, CheckedAt: &now, Message: "No matching stored revision.", HealthMessage: "Readiness observation."}
		if err := a.store.SaveDriftCheck(ctx, app.ID, check); err != nil {
			t.Fatal(err)
		}
		observed, err := a.applicationSync(ctx, app.ID)
		if err != nil || observed.Drift.CheckedAt == nil || observed.Drift.Health != health || observed.Drift.Message != check.Message || observed.Drift.HealthMessage != check.HealthMessage || observed.ReapplyAvailable {
			t.Fatalf("lost observation without baseline: %+v %v", observed, err)
		}
	}
	serviceRequestTest(t, a, "POST", "/api/v1/apps/"+app.ID+"/reapply", map[string]string{"deploymentId": d.ID}, 409)
	user := core.User{ID: "drift-member", Username: "drift-member", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "drift-role", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: project.ID, Role: "viewer", CreatedAt: now, UpdatedAt: now}
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	call := func(permission core.Permission) int {
		request := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
		request = request.WithContext(withIdentity(request.Context(), core.Identity{ID: user.ID, SystemRole: "member"}))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app.ID)
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
		response := httptest.NewRecorder()
		a.appPermission(permission)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(response, request)
		return response.Code
	}
	if call(core.PermissionProjectView) != 204 || call(core.PermissionProjectConfigure) != 403 || call(core.PermissionDeploymentRun) != 403 {
		t.Fatal("viewer role bypass")
	}
	grant.Role = "deployer"
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if call(core.PermissionDeploymentRun) != 204 || call(core.PermissionProjectConfigure) != 403 {
		t.Fatal("deployer role incorrect")
	}
	grant.Role = "operator"
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if call(core.PermissionProjectConfigure) != 204 {
		t.Fatal("operator cannot check")
	}
	if err := a.store.DeleteRoleAssignment(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateProject(ctx, core.Project{ID: "another-project", Name: "another-project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	grant.ScopeID = "another-project"
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if call(core.PermissionProjectView) != 403 || call(core.PermissionDeploymentRun) != 403 {
		t.Fatal("cross-project access")
	}
}
func TestSyncDoesNotReuseGreenResultForNewDeployment(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, _ := a.store.ListApps(ctx)
	app := apps[0]
	now := time.Now().UTC()
	d := core.Deployment{ID: "new-success", AppID: app.ID, State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CreatedAt: now}
	if err := a.store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := a.store.SaveDriftCheck(ctx, app.ID, core.DriftCheck{DeploymentID: "old-success", State: "synced", Health: "healthy", CheckedAt: &now}); err != nil {
		t.Fatal(err)
	}
	status, err := a.applicationSync(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Drift.State == "synced" || status.Drift.Health == "healthy" {
		t.Fatal("stale green observation survived deployment", status)
	}
}

func TestApplicationSyncReportsConfigurationFailure(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	d := core.Deployment{ID: "sync-error-deployment", AppID: app.ID, State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CreatedAt: now}
	must(a.store.CreateDeployment(ctx, d))
	secret := core.Secret{ID: "sync-error-secret", Name: "sync-error-secret", Type: core.SecretTypeGitHubToken, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateSecret(ctx, secret))
	source := core.ConfigSource{ID: "sync-error-source", Name: "Configurations", ProjectID: app.ProjectID, CredentialSecretID: secret.ID, Active: true, State: "degraded", LastError: "Another application has invalid configuration.", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateConfigSource(ctx, source))
	resource := core.WorkflowResource{ID: "sync-error-resource", Name: "api", ConfigSourceID: source.ID, Kind: "Application", Active: true, State: "invalid", LastError: `deployment/api.yaml: source service (main): branch or tag "main" was not found in Example/service`, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateWorkflowResource(ctx, resource))
	revision := core.WorkflowRevision{ID: "sync-error-revision", ResourceID: resource.ID, State: "succeeded", CreatedAt: now}
	must(a.store.CreateWorkflowRevision(ctx, revision))
	must(a.store.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "sync-error-stage", RevisionID: revision.ID, StageName: "dev", State: "succeeded", DeploymentIDs: []string{d.ID}, CreatedAt: now}))
	raw := serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/sync", nil, 200)
	var status applicationSync
	must(json.Unmarshal(raw, &status))
	if status.Configuration.State != "invalid" || status.Configuration.Message != resource.LastError {
		t.Fatalf("configuration cause lost: %+v", status.Configuration)
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/deployment-catalog", nil, 200)
	var catalog struct {
		Items []deploymentCatalogItem `json:"items"`
	}
	must(json.Unmarshal(raw, &catalog))
	found := false
	for _, item := range catalog.Items {
		if item.AppID == app.ID {
			found = true
			if item.Sync == nil || item.Sync.ConfigurationMessage != resource.LastError {
				t.Fatalf("catalog lost configuration cause: %+v", item.Sync)
			}
		}
	}
	if !found {
		t.Fatal("application missing from catalog")
	}
	for _, test := range []struct{ name, resourceState, resourceError, sourceError, want string }{
		{"source fallback", "invalid", " ", "Repository access denied.", "Repository access denied."},
		{"source warning", "ready", "Old error", "Another application is invalid.", "Another application is invalid."},
		{"missing detail", "invalid", "", "", "Configuration sync failed without an error detail. Sync the repository configuration again to get the current cause."},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource.State, resource.LastError, source.LastError = test.resourceState, test.resourceError, test.sourceError
			if got := configurationSyncError(resource, source); got != test.want {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

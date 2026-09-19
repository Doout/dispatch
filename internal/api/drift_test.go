package api

import (
	"context"
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
		a.appPermission(permission, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(response, request)
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

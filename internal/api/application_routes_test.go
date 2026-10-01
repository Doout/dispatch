package api

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplicationRoutePermissionsAndServingConfigProtection(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal(err)
	}
	app := apps[0]
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.Runtime = core.ServerRuntimeDocker
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	app.Domain = ""
	app.BuildType = core.BuildTypeDockerfile
	app.ContainerPort = 8080
	if err = a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/servers/" + server.ID + "/routing"
	config := map[string]any{"routing": map[string]any{"baseDomain": "apps.example.com", "requireTls": true, "tlsResolver": "letsencrypt"}}
	serviceRequestTest(t, a, "PUT", path, config, 200)
	server, err = a.store.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := routing.Plan(core.Deployment{ID: "candidate"}, app, server)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.(store.ApplicationRouteStore).ReserveApplicationRoute(ctx, *plan); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", path, map[string]any{"routing": nil}, 409)
	serviceRequestTest(t, a, "PUT", path, map[string]any{"routing": map[string]any{"baseDomain": "apps.example.com", "entryPoint": "other", "requireTls": true, "tlsResolver": "letsencrypt"}}, 409)
	now := time.Now().UTC()
	user := core.User{ID: "route-viewer", Username: "route-viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err = a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "route-role", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: app.ProjectID, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		method, path string
		status       int
	}{{"GET", "/api/v1/apps/" + app.ID + "/route", 200}, {"POST", "/api/v1/apps/" + app.ID + "/route/check", 403}, {"PUT", path, 403}} {
		req := tokenRequest(check.method, check.path, strings.NewReader(`{"routing":null}`))
		req.Header.Set("Impersonate-User", user.ID)
		response := httptest.NewRecorder()
		a.ServeHTTP(response, req)
		if response.Code != check.status {
			t.Fatalf("%s %s: %d %s", check.method, check.path, response.Code, response.Body.String())
		}
	}
}

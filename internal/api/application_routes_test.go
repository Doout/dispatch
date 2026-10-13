package api

import (
	"context"
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instanceconnection"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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

func TestHostedRoutingScopeIsAssignedByPlatform(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := t.Context()
	servers, err := a.store.ListServers(ctx)
	if err != nil || len(servers) == 0 {
		t.Fatal(err)
	}
	server := servers[0]
	server.Runtime = core.ServerRuntimeDocker
	if err := a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	// Exercise the handler with the existing authenticated owner fixture. The
	// hosted boundary is unrelated to this handler's routing validation.
	path := "/api/v1/servers/" + server.ID + "/routing"
	spoof := map[string]any{"routing": map[string]any{"baseDomain": "doodship.com", "hosted": map[string]any{"tenantId": "foreign", "tenantSlug": "foreign", "rootDomain": "doodship.com"}}}
	serviceRequestTest(t, a, "PUT", path, spoof, 422)
	a.auth.Hosted = &HostedAuth{RoutingScope: &core.HostedRoutingScope{TenantID: "tenant", TenantSlug: "alpha", RootDomain: "doodship.com"}}
	r := httptest.NewRequest("PUT", path, strings.NewReader(`{"routing":{"baseDomain":"custom.example.com","entryPoint":"web","requireTls":false,"tlsResolver":"user-resolver"}}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("id", server.ID)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeContext))
	w := httptest.NewRecorder()
	a.updateServerRouting(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	saved, err := a.store.GetServer(ctx, server.ID)
	if err != nil || saved.Routing.Hosted.TenantID != "tenant" || saved.Routing.BaseDomain != "doodship.com" || !saved.Routing.RequireTLS || saved.Routing.TLSResolver != "" {
		t.Fatal(saved, err)
	}
}

func TestHostedCustomApplicationDomainRejectedBeforeCreation(t *testing.T) {
	a := serviceTestAPI(t)
	a.auth.Hosted = &HostedAuth{TenantID: "tenant"}
	r := httptest.NewRequest("POST", "/api/v1/apps", strings.NewReader(`{"name":"Application","domain":"custom.example.com","buildType":"dockerfile"}`))
	w := httptest.NewRecorder()
	a.createApp(w, r)
	if w.Code != 422 || !strings.Contains(w.Body.String(), "assigned automatically") {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestConnectedRoutingPreservesLocalDomainAndAssignsSelectedManagedScope(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := t.Context()
	manager, err := instanceconnection.Open(instanceconnection.Config{File: filepath.Join(t.TempDir(), "connection.json"), AllowInsecure: true, LookupUser: a.store.GetUser})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Stop()
	var cp *httptest.Server
	cp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"instanceID": "instance", "generation": 1, "credential": "connection-only", "instanceSlug": "alpha", "rootDomain": "doodship.test", "websocketURL": strings.Replace(cp.URL, "http:", "ws:", 1) + "/api/v1/instance-connections/ws"})
	}))
	defer cp.Close()
	if _, err = manager.Configure(ctx, instanceconnection.Setup{ControlPlaneURL: cp.URL, EnrollmentToken: "once"}); err != nil {
		t.Fatal(err)
	}
	a.eventConfig.InstanceConnection = manager
	servers, err := a.store.ListServers(ctx)
	if err != nil || len(servers) == 0 {
		t.Fatal(err)
	}
	server := servers[0]
	server.Runtime = core.ServerRuntimeDocker
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	endpoint := "/api/v1/servers/" + server.ID + "/routing"
	serviceRequestTest(t, a, "PUT", endpoint, map[string]any{"routing": map[string]any{"baseDomain": "apps.customer.test", "requireTls": true, "tlsResolver": "customer"}}, 200)
	local, err := a.store.GetServer(ctx, server.ID)
	if err != nil || local.Routing.Hosted != nil || local.Routing.BaseDomain != "apps.customer.test" || local.Routing.TLSResolver != "customer" {
		t.Fatal(local, err)
	}
	serviceRequestTest(t, a, "PUT", endpoint, map[string]any{"routing": map[string]any{"baseDomain": "Doodship.Test.", "requireTls": false, "tlsResolver": "customer"}}, 200)
	managed, err := a.store.GetServer(ctx, server.ID)
	if err != nil || managed.Routing.Hosted == nil || managed.Routing.Hosted.TenantID != "instance" || managed.Routing.BaseDomain != "doodship.test" || !managed.Routing.RequireTLS || managed.Routing.TLSResolver != "" {
		t.Fatal(managed, err)
	}
	serviceRequestTest(t, a, "PUT", endpoint, map[string]any{"routing": map[string]any{"baseDomain": "doodship.test", "hosted": map[string]any{"tenantId": "foreign", "tenantSlug": "foreign", "rootDomain": "doodship.test"}}}, 422)
}

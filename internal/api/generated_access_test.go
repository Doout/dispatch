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

func TestGeneratedApplicationsRespectProjectVisibility(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	a := handler.(*API)
	ctx := context.Background()
	now := time.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := core.User{ID: "viewer", Username: "viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateUser(ctx, user))
	for _, id := range []string{"allowed", "hidden", "closed"} {
		must(a.store.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}))
		must(a.store.CreateServer(ctx, core.Server{ID: id, Name: id, Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: now}))
		state := "ready"
		if id == "closed" {
			state = "closed"
		}
		must(a.store.CreateApp(ctx, core.App{ID: id, ProjectID: id, ServerID: id, Name: id, Generated: true, State: state, CreatedAt: now}))
		must(a.store.CreateDeployment(ctx, core.Deployment{ID: id, AppID: id, State: core.DeploymentSucceeded, CreatedAt: now}))
	}
	for _, id := range []string{"allowed", "closed"} {
		must(a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: id, PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: id, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}))
	}
	must(a.store.CreateApp(ctx, core.App{ID: "hidden-neighbor", ProjectID: "hidden", ServerID: "allowed", Name: "Hidden neighbor", Generated: true, State: "ready", CreatedAt: now}))
	must(a.store.CreateDeployment(ctx, core.Deployment{ID: "hidden-neighbor", AppID: "hidden-neighbor", State: core.DeploymentSucceeded, CreatedAt: now}))
	request := httptest.NewRequest("GET", "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Impersonate-User", user.ID)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("overview: %d %s", response.Code, response.Body.String())
	}
	var overview core.Overview
	must(json.Unmarshal(response.Body.Bytes(), &overview))
	if len(overview.Servers) != 1 || overview.Servers[0].ID != "allowed" {
		t.Fatalf("wrong servers: %+v", overview.Servers)
	}
	if len(overview.Deployments) != 1 || overview.Deployments[0].ID != "allowed" {
		t.Fatalf("wrong deployment visibility: %+v", overview.Deployments)
	}
	if len(overview.Apps) != 0 {
		t.Fatal("generated applications duplicated in standalone list")
	}
	for _, id := range []string{"allowed", "hidden", "closed"} {
		r := httptest.NewRequest("GET", "/", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		r = r.WithContext(context.WithValue(withIdentity(r.Context(), identityForUser(user)), chi.RouteCtxKey, route))
		w := httptest.NewRecorder()
		a.serverPermission(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })(w, r)
		want := 403
		if id == "allowed" {
			want = 204
		}
		if w.Code != want {
			t.Fatalf("server %s: got %d want %d", id, w.Code, want)
		}
	}
	request = httptest.NewRequest("GET", "/api/v1/servers/allowed/topology", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Impersonate-User", user.ID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || strings.Contains(response.Body.String(), "hidden-neighbor") || !strings.Contains(response.Body.String(), "release:allowed") {
		t.Fatalf("shared target topology crossed project boundary: %d %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest("DELETE", "/api/v1/servers/allowed", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Impersonate-User", user.ID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 403 {
		t.Fatalf("viewer gained server management: %d", response.Code)
	}
}

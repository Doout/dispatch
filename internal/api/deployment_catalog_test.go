package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func TestDeploymentCatalogRetainsRunningReleaseAndSearchesOlderHistory(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	app.ID = "catalog-generated-app"
	app.Name = "Generated development environment"
	app.Generated = true
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(time.Hour)
	for i := 0; i < 130; i++ {
		state := core.DeploymentFailed
		if i == 0 {
			state = core.DeploymentSucceeded
		}
		d := core.Deployment{ID: fmt.Sprintf("catalog-%03d", i), AppID: app.ID, State: state, CommitSHA: fmt.Sprintf("revision-%03d", i), CreatedAt: now.Add(time.Duration(i) * time.Second), Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, TargetName: "original target", Values: map[string]any{"password": "catalog-secret"}}, Outputs: map[string]string{"TOKEN": "catalog-secret"}}
		if err = a.store.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/deployment-catalog", nil, 200)
	if strings.Contains(string(raw), "catalog-secret") || strings.Contains(string(raw), "spec_snapshot") {
		t.Fatal("catalog exposed sensitive inputs")
	}
	var catalog struct {
		Items []deploymentCatalogItem `json:"items"`
	}
	if err = json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, item := range catalog.Items {
		if item.AppID == app.ID {
			found = true
			if item.Latest == nil || item.Latest.ID != "catalog-129" || item.Current == nil || item.Current.ID != "catalog-000" {
				t.Fatalf("latest attempt obscured running release: %+v", item)
			}
		}
	}
	if !found {
		t.Fatal("application absent")
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/deployment-search?application="+app.ID+"&q=revision-000", nil, 200)
	if !strings.Contains(string(raw), "catalog-000") {
		t.Fatal("search omitted run outside overview window")
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/deployment-search?application="+app.ID+"&status=failed", nil, 200)
	var page struct {
		Items []core.Deployment `json:"items"`
		Next  string            `json:"next"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || page.Items[0].ID != "catalog-129" || page.Next != "catalog-080" {
		t.Fatalf("bad first page %+v", page)
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/deployment-search?application="+app.ID+"&status=failed&before="+page.Next, nil, 200)
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if page.Items[0].ID != "catalog-079" {
		t.Fatal("unstable page cursor")
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/deployments/catalog-000/identity", nil, 200)
	var identity deploymentCatalogItem
	if err = json.Unmarshal(raw, &identity); err != nil {
		t.Fatal(err)
	}
	if identity.TargetID != app.ServerID || identity.Current.ID != "catalog-000" {
		t.Fatal("incorrect saved identity")
	}
}

func TestDeploymentCatalogProjectIsolationAndEnvironmentComparison(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, _ := a.store.ListApps(ctx)
	base := apps[0]
	now := time.Now().UTC()
	user := core.User{ID: "catalog-viewer", Username: "catalog-viewer", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "catalog-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: base.ProjectID, Role: "viewer", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	private := core.Project{ID: "catalog-private", Name: "private", CreatedAt: now}
	if err := a.store.CreateProject(ctx, private); err != nil {
		t.Fatal(err)
	}
	other := base
	other.ID = "catalog-private-app"
	other.ProjectID = private.ID
	other.Name = "private-hidden"
	other.Generated = true
	if err := a.store.CreateApp(ctx, other); err != nil {
		t.Fatal(err)
	}
	stage := base
	stage.ID = "catalog-stage"
	stage.Name = "production"
	stage.Generated = true
	if err := a.store.CreateApp(ctx, stage); err != nil {
		t.Fatal(err)
	}
	for i, app := range []core.App{base, stage, other} {
		if err := a.store.CreateDeployment(ctx, core.Deployment{ID: fmt.Sprintf("catalog-env-%d", i), AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: map[string]any{"replicas": i, "password": "catalog-private-password"}}}); err != nil {
			t.Fatal(err)
		}
	}
	call := func(handler http.HandlerFunc, path, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req = req.WithContext(withIdentity(req.Context(), core.Identity{ID: user.ID, SystemRole: "member"}))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}
	rr := call(a.deploymentCatalog, "/", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "private-hidden") {
		t.Fatalf("catalog isolation: %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "catalog-stage") {
		t.Fatal("generated application missing from catalog")
	}
	rr = call(a.deploymentSearch, "/?q=private-hidden", "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "catalog-env-2") {
		t.Fatal("search crossed project boundary")
	}
	rr = call(a.deploymentSearch, "/?before=catalog-env-2", "")
	if rr.Code != 404 {
		t.Fatal("private cursor accepted")
	}
	rr = call(a.deploymentPermission(core.PermissionProjectView, a.compareEnvironments), "/?from=catalog-env-0", "catalog-env-1")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "/values/replicas") || strings.Contains(rr.Body.String(), "catalog-private-password") {
		t.Fatalf("cross environment comparison: %d %s", rr.Code, rr.Body.String())
	}
	rr = call(a.deploymentPermission(core.PermissionProjectView, a.compareEnvironments), "/?from=catalog-env-2", "catalog-env-1")
	if rr.Code != 404 {
		t.Fatal("cross project comparison accepted")
	}
}

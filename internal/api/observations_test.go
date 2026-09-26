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
	"github.com/doout/dispatch/internal/observe"
	"github.com/go-chi/chi/v5"
)

func TestObservationAPIWriteOnlyAndProjectRoles(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal(err)
	}
	app := apps[0]
	body := map[string]any{"revision": 0, "scheduled": false, "intervalSeconds": 300, "staleAfterSeconds": 900, "notificationsEnabled": true, "webhookUrl": "https://hooks.example.com/private-secret"}
	raw := serviceRequestTest(t, a, http.MethodPut, "/api/v1/apps/"+app.ID+"/observations", body, 200)
	if strings.Contains(string(raw), "private-secret") || strings.Contains(string(raw), "ciphertext") {
		t.Fatal("webhook exposed")
	}
	var status observe.Status
	if err = json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	if !status.Configuration.WebhookConfigured {
		t.Fatal(status)
	}
	delete(body, "webhookUrl")
	body["revision"] = status.Configuration.Revision
	raw = serviceRequestTest(t, a, http.MethodPut, "/api/v1/apps/"+app.ID+"/observations", body, 200)
	if !strings.Contains(string(raw), `"webhookConfigured":true`) {
		t.Fatal("omitted webhook not retained")
	}
	now := time.Now().UTC()
	user := core.User{ID: "observation-user", Username: "observation-user", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "observation-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: app.ProjectID, Role: "viewer", CreatedAt: now, UpdatedAt: now}
	call := func(permission core.Permission) int {
		r := httptest.NewRequest("GET", "/", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", app.ID)
		r = r.WithContext(context.WithValue(withIdentity(r.Context(), identityForUser(user)), chi.RouteCtxKey, route))
		rr := httptest.NewRecorder()
		a.appPermission(permission)(http.HandlerFunc(a.getApplicationObservations)).ServeHTTP(rr, r)
		return rr.Code
	}
	for _, role := range []string{"viewer", "deployer", "operator", "admin"} {
		grant.Role = role
		if err = a.store.UpsertRoleAssignment(ctx, grant); err != nil {
			t.Fatal(err)
		}
		if call(core.PermissionProjectView) != 200 {
			t.Fatal(role, "cannot view")
		}
		want := 403
		if role == "operator" || role == "admin" {
			want = 200
		}
		if code := call(core.PermissionProjectConfigure); code != want {
			t.Fatal(role, code)
		}
	}
	if err = a.store.DeleteRoleAssignment(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if call(core.PermissionProjectView) != 403 {
		t.Fatal("project boundary bypass")
	}
}

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestHealthPolicyAPIValidationAndProjectPermissions(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal(err)
	}
	app := apps[0]
	app.ID, app.Name = "health-api-app", "Health API"
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/apps/" + app.ID + "/health-policy"
	serviceRequestTest(t, a, "PUT", path, map[string]any{"timeoutSeconds": 30, "failureThreshold": 2, "checks": []map[string]any{{"id": "ready", "kind": "http", "path": "/ready", "port": 8080}}}, 200)
	saved, err := a.store.GetApp(ctx, app.ID)
	if err != nil || saved.HealthPolicy.TimeoutSeconds != 30 || len(saved.HealthPolicy.Checks) != 2 {
		t.Fatalf("policy not saved: %+v %v", saved.HealthPolicy, err)
	}
	serviceRequestTest(t, a, "PUT", path, map[string]any{"timeoutSeconds": 601}, 422)
	serviceRequestTest(t, a, "PUT", path, map[string]any{"checks": []map[string]any{{"id": "private", "kind": "http", "path": "/ready?token=secret"}}}, 422)
	now := time.Now().UTC()
	user := core.User{ID: "health-viewer", Username: "health-viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err = a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "health-viewer-role", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: app.ProjectID, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		req := tokenRequest(method, path, strings.NewReader(`{"timeoutSeconds":60}`))
		req.Header.Set("Impersonate-User", user.ID)
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, req)
		want := 200
		if method == "PUT" {
			want = 403
		}
		if rr.Code != want {
			t.Fatalf("viewer %s: %d %s", method, rr.Code, rr.Body.String())
		}
	}
}

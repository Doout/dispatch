package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestOperationsAuditIsolationAndExpiredGrants(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	project := projects[0]
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	u := core.User{ID: "ops-viewer", Username: "ops-viewer", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "ops-viewer-grant", PrincipalType: "user", PrincipalID: u.ID, ScopeType: "project", ScopeID: project.ID, Role: "viewer", CreatedAt: now, UpdatedAt: now, ExpiresAt: &future}
	if err = a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	data := a.store.(operationsStore)
	for _, e := range []core.AuditEvent{{ID: "ops-event-a", ProjectID: project.ID, ActorID: "alice", Action: "PUT /apps/{id}", Outcome: "succeeded", CreatedAt: now}, {ID: "ops-event-b", ProjectID: "private-project", ActorID: "private-actor", Action: "DELETE /projects/{id}", Outcome: "succeeded", CreatedAt: now}} {
		if err = data.AppendAuditEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	asViewer := func(method, path string, body any, want int) []byte {
		t.Helper()
		payload, _ := json.Marshal(body)
		req := tokenRequest(method, path, bytes.NewReader(payload))
		req.Header.Set("Impersonate-User", u.ID)
		res := httptest.NewRecorder()
		a.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s: %d %s", path, res.Code, res.Body.String())
		}
		return res.Body.Bytes()
	}
	out := asViewer("GET", "/api/v1/audit", nil, 200)
	if strings.Contains(string(out), "private-actor") || !strings.Contains(string(out), "alice") {
		t.Fatal("audit visibility incorrect")
	}
	asViewer("POST", "/api/v1/operations/backups", nil, 403)
	asViewer("POST", "/api/v1/projects/"+project.ID+"/retention/apply", map[string]any{"confirm": project.ID}, 403)
	grant.ExpiresAt = &past
	if err = a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	asViewer("GET", "/api/v1/audit?projectId="+project.ID, nil, 403)
	out = asViewer("GET", "/api/v1/audit", nil, 200)
	if strings.Contains(string(out), "alice") {
		t.Fatal("expired grant retained audit access")
	}
}
func TestOperationsAuditNeverRecordsRequestCredentials(t *testing.T) {
	a := serviceTestAPI(t)
	projects, _ := a.store.ListProjects(context.Background())
	serviceRequestTest(t, a, "POST", "/api/v1/services", map[string]any{"projectId": projects[0].ID, "name": "audit-safe", "type": "generic", "fields": map[string]any{"token": map[string]any{"value": "private-audit-fixture-value", "sensitive": true}}}, 201)
	out := serviceRequestTest(t, a, "GET", "/api/v1/audit", nil, 200)
	if strings.Contains(string(out), "private-audit-fixture-value") || strings.Contains(string(out), "encryptedValue") {
		t.Fatal("request credential reached audit")
	}
	if !strings.Contains(string(out), "POST /api/v1/services") {
		t.Fatalf("mutation not audited: %s", out)
	}
}
func TestOperationsRetentionRequiresConfirmationAndOwnerMetadata(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	apps, _ := a.store.ListApps(ctx)
	project := projects[0]
	path := "/api/v1/projects/" + project.ID + "/retention"
	serviceRequestTest(t, a, "PUT", path, map[string]any{"logDays": 0, "runDays": 1, "keepRuns": 1}, 400)
	serviceRequestTest(t, a, "POST", path+"/apply", map[string]any{"confirm": "wrong"}, 400)
	serviceRequestTest(t, a, "POST", path+"/preview", nil, 200)
	if len(apps) > 0 {
		serviceRequestTest(t, a, "PUT", "/api/v1/apps/"+apps[0].ID+"/owner", map[string]any{"principalType": "user", "principalId": "missing"}, 400)
		serviceRequestTest(t, a, "PUT", "/api/v1/apps/"+apps[0].ID+"/owner", map[string]any{"principalType": "user", "principalId": ""}, 200)
	}
}

func TestRoleUpdatesPreserveOmittedExpiryAndAllowExplicitRemoval(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	now := time.Now().UTC()
	u := core.User{ID: "expiry-user", Username: "expiry-user", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(time.Hour).Format(time.RFC3339Nano)
	input := map[string]any{"principalType": "user", "principalId": u.ID, "projectId": projects[0].ID, "role": "viewer", "expiresAt": expiry}
	first := serviceRequestTest(t, a, "POST", "/api/v1/role-assignments", input, 200)
	var saved core.RoleAssignment
	if err := json.Unmarshal(first, &saved); err != nil {
		t.Fatal(err)
	}
	id := saved.ID
	delete(input, "expiresAt")
	input["role"] = "operator"
	second := serviceRequestTest(t, a, "POST", "/api/v1/role-assignments", input, 200)
	saved = core.RoleAssignment{}
	if err := json.Unmarshal(second, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ID != id || saved.ExpiresAt == nil || saved.ExpiresAt.Format(time.RFC3339Nano) != expiry {
		t.Fatal("role update removed omitted expiry or returned wrong identity")
	}
	input["expiresAt"] = nil
	third := serviceRequestTest(t, a, "POST", "/api/v1/role-assignments", input, 200)
	saved = core.RoleAssignment{}
	if err := json.Unmarshal(third, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.ExpiresAt != nil {
		t.Fatal("explicit null did not clear expiry")
	}
}

func TestApplicationOwnershipRejectsUnrelatedProjectPrincipals(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal(err)
	}
	app := apps[0]
	now := time.Now().UTC()
	user := core.User{ID: "owner-unrelated", Username: "owner-unrelated", DisplayName: "Unrelated person", State: "active", SystemRole: "member", CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"principalType": "user", "principalId": user.ID}
	path := "/api/v1/apps/" + app.ID + "/owner"
	serviceRequestTest(t, a, "PUT", path, body, 400)
	grant := core.RoleAssignment{ID: "owner-project-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: app.ProjectID, Role: "viewer", CreatedAt: now, UpdatedAt: now}
	if err = a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", path, body, 200)
	user.State = "disabled"
	if err = a.store.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", path, body, 400)
}

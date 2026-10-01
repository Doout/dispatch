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
	"github.com/doout/dispatch/internal/store"
)

func automationRequest(t *testing.T, a *API, token, method, path string, body any, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w
}
func TestAutomationCredentialAuthorizationAndRotation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	servers, _ := a.store.ListServers(ctx)
	project := projects[0]
	now := time.Now().UTC()
	other := core.Project{ID: "automation-other", Name: "Other", CreatedAt: now}
	if err := a.store.CreateProject(ctx, other); err != nil {
		t.Fatal(err)
	}
	var account core.ServiceAccount
	w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "deploy-pipeline"}, 201)
	json.Unmarshal(w.Body.Bytes(), &account)
	issuePath := "/api/v1/automation-accounts/" + account.ID + "/credentials"
	input := map[string]any{"name": "ci", "expiresAt": now.Add(time.Hour)}
	var issued struct {
		Credential core.AutomationCredential `json:"credential"`
		Token      string                    `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", issuePath, input, 201)
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil || !strings.HasPrefix(issued.Token, "dsa_") {
		t.Fatal("token issuance", err)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token may be cached")
	}
	for _, path := range []string{"/api/v1/automation-accounts", issuePath} {
		w = automationRequest(t, a, "secret", "GET", path, nil, 200)
		if strings.Contains(w.Body.String(), issued.Token) || strings.Contains(w.Body.String(), sessionHash(issued.Token)) {
			t.Fatal("token leaked")
		}
	}
	automationRequest(t, a, issued.Token, "GET", "/api/v1/automation-accounts", nil, 403)
	automationRequest(t, a, issued.Token, "POST", "/api/v1/infrastructure/providers", map[string]any{}, 403)
	automationRequest(t, a, issued.Token, "PUT", "/api/v1/auth/password", map[string]any{}, 403)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project.ID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun, core.PermissionInfrastructureInspect, core.PermissionInfrastructureCreate}}
	automationRequest(t, a, issued.Token, "PUT", "/api/v1/infrastructure/grants", grant, 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	// A deployment grant never implies deletion, restore, or human approval.
	identity := core.Identity{Kind: core.PrincipalServiceAccount, ID: account.ID, SystemRole: core.UserRoleMember}
	scope := withIdentity(ctx, identity)
	for _, p := range []core.Permission{core.PermissionInfrastructureDelete, core.PermissionSnapshotRestore, core.PermissionStageApprove} {
		if allowed, err := a.canProject(scope, p, project.ID); err != nil || allowed {
			t.Fatal("inferred permission", p, err)
		}
	}
	invalid := grant
	invalid.Permissions = []core.Permission{core.PermissionStageApprove}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", invalid, 422)
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/overview", nil, 200)
	var overview core.Overview
	if err := json.Unmarshal(w.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Projects) != 1 || overview.Projects[0].ID != project.ID || len(overview.ProjectPermissions[project.ID]) != len(grant.Permissions) {
		t.Fatalf("overview scope %#v", overview.ProjectPermissions)
	}
	automationRequest(t, a, issued.Token, "GET", "/api/v1/infrastructure/assignments/"+other.ID, nil, 403)
	appInput := map[string]any{"projectId": project.ID, "serverId": servers[0].ID, "name": "agent-app", "buildType": "docker_compose", "composeContent": "services:\n  web:\n    image: busybox:1.37\n"}
	automationRequest(t, a, issued.Token, "POST", "/api/v1/apps", appInput, 403)
	automationRequest(t, a, issued.Token, "GET", "/api/v1/servers?projectId="+other.ID, nil, 403)
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/servers?projectId="+project.ID, nil, 200)
	var assignedTargets []core.Server
	json.Unmarshal(w.Body.Bytes(), &assignedTargets)
	if len(assignedTargets) != 0 {
		t.Fatal("unassigned target exposed")
	}
	human := httptest.NewRecorder()
	a.approveWorkflowStage(human, httptest.NewRequest("POST", "/", strings.NewReader(`{"confirm":true}`)).WithContext(scope))
	if human.Code != 403 {
		t.Fatal("automation supplied human approval")
	}

	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": "target", "resourceId": servers[0].ID}, 200)
	w = automationRequest(t, a, issued.Token, "POST", "/api/v1/apps", appInput, 201)
	var app core.App
	json.Unmarshal(w.Body.Bytes(), &app)
	otherApp := app
	otherApp.ID = "automation-other-app"
	otherApp.Name = "another"
	otherApp.ProjectID = other.ID
	if err := a.store.CreateApp(ctx, otherApp); err != nil {
		t.Fatal(err)
	}
	automationRequest(t, a, issued.Token, "GET", "/api/v1/apps/"+otherApp.ID+"/runtime/jobs/unknown", nil, 403)
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/apps", nil, 200)
	if strings.Contains(w.Body.String(), otherApp.ID) {
		t.Fatal("cross-project inventory")
	}
	automationRequest(t, a, issued.Token, "POST", "/api/v1/apps/"+otherApp.ID+"/deployments", map[string]any{}, 403)
	automationRequest(t, a, issued.Token, "POST", "/api/v1/apps/"+app.ID+"/deployments", map[string]any{}, 202)
	// Header-based impersonation remains unavailable even with valid credentials.
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	req.Header.Set("Impersonate-User", account.ID)
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatal("impersonation accepted", rr.Code)
	}
	old := issued
	w = automationRequest(t, a, "secret", "POST", issuePath+"/"+old.Credential.ID+"/rotate", input, 201)
	json.Unmarshal(w.Body.Bytes(), &issued)
	automationRequest(t, a, old.Token, "GET", "/api/v1/auth/me", nil, 401)
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/auth/me", nil, 200)
	if !strings.Contains(w.Body.String(), account.ID) || !strings.Contains(w.Body.String(), issued.Credential.ID) {
		t.Fatal("rotation lost identity")
	}
	automationRequest(t, a, "secret", "POST", issuePath+"/"+issued.Credential.ID+"/revoke", nil, 204)
	automationRequest(t, a, issued.Token, "GET", "/api/v1/auth/me", nil, 401)
	w = automationRequest(t, a, "secret", "POST", issuePath, input, 201)
	json.Unmarshal(w.Body.Bytes(), &issued)
	automationRequest(t, a, "secret", "PUT", "/api/v1/automation-accounts/"+account.ID, map[string]any{"name": account.Name, "state": "disabled"}, 200)
	automationRequest(t, a, issued.Token, "GET", "/api/v1/auth/me", nil, 401)
	data := a.store.(store.AutomationStore)
	past := now.Add(-time.Second)
	grant.ExpiresAt = &past
	if err := data.SavePrincipalGrant(ctx, grant); err != nil {
		t.Fatal(err)
	}
	if allowed, err := a.canProject(scope, core.PermissionDeploymentRun, project.ID); err != nil || allowed {
		t.Fatal("expired grant accepted", err)
	}
	audit := a.store.(interface {
		ListAuditEvents(context.Context, core.AuditFilter) ([]core.AuditEvent, error)
	})
	events, err := audit.ListAuditEvents(ctx, core.AuditFilter{ActorID: account.ID})
	if err != nil || len(events) == 0 {
		t.Fatal("missing audit", err)
	}
	for _, e := range events {
		if e.ActorType != core.PrincipalServiceAccount || e.CredentialID == "" {
			t.Fatal("actor evidence absent", e)
		}
	}
	raw, _ := json.Marshal(events)
	for _, secret := range []string{old.Token, issued.Token, sessionHash(old.Token)} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("credential in audit")
		}
	}
}

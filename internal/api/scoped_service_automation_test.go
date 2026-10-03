package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
)

func scopedServiceIdentity(t *testing.T, a *API, project string, permissions ...core.Permission) (string, core.PrincipalGrant) {
	t.Helper()
	var account core.ServiceAccount
	w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "service-runner"}, 201)
	if err := json.Unmarshal(w.Body.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project, Permissions: permissions}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "services", "expiresAt": time.Now().UTC().Add(time.Hour)}, 201)
	var issued struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	return issued.Token, grant
}
func requireScopedStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestScopedServiceProvisionRequiresApprovalTargetQuotaAndReceipt(t *testing.T) {
	a, runtime, template := resourceAPIFixture(t)
	path := "/api/v1/service-templates/" + template.ID + "/runs"
	token, grant := scopedServiceIdentity(t, a, template.ProjectID, core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun)
	request := func(key, name string) *httptest.ResponseRecorder {
		return mutationRequest(a, token, "POST", path, key, map[string]any{"name": name})
	}
	requireScopedStatus(t, request("scoped-create", "scoped-db"), 403)
	grant.Permissions = []core.Permission{core.PermissionProjectView, core.PermissionServiceProvision}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	requireScopedStatus(t, request("", "scoped-db"), 422)
	requireScopedStatus(t, request("scoped-create", "scoped-db"), 403)
	approval := map[string]any{"kind": "service_template", "resourceId": template.ID + "@" + template.Digest}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+template.ProjectID, approval, 200)
	requireScopedStatus(t, request("scoped-create", "scoped-db"), 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+template.ProjectID, map[string]any{"kind": "target", "resourceId": "resource-target"}, 200)
	denied := request("quota-required", "scoped-db")
	requireScopedStatus(t, denied, 409)
	policy := core.InfrastructureQuotaPolicy{MaxServices: 1, Providers: []core.InfrastructureProviderRule{}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+template.ProjectID+"/infrastructure/quota", policy, 200)
	w := request("scoped-create", "scoped-db")
	requireScopedStatus(t, w, 202)
	receipt := decodeMutation(t, w)
	awaitServiceResource(t, a, receipt.OperationID, "ready")
	replay := request("scoped-create", "scoped-db")
	requireScopedStatus(t, replay, 202)
	if decodeMutation(t, replay).OperationID != receipt.OperationID {
		t.Fatal("retry allocated another service")
	}
	requireScopedStatus(t, request("scoped-another", "another-db"), 409)
	runtime.mu.Lock()
	count := runtime.created
	runtime.mu.Unlock()
	if count != 1 {
		t.Fatal("unexpected resource count", count)
	}
	automationRequest(t, a, token, "PUT", "/api/v1/service-templates/"+template.ID, map[string]any{}, 403)
	automationRequest(t, a, token, "POST", "/api/v1/service-templates", map[string]any{}, 403)
	// Editing the definition invalidates the owner's assignment of its old digest.
	documents, err := workflow.Parse("template.yaml", []byte(template.Document))
	if err != nil {
		t.Fatal(err)
	}
	documents[0].ServiceTemplate.Description = "Changed approved definition"
	changed, err := documents[0].MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/service-templates/"+template.ID, map[string]any{"revision": template.Revision, "projectId": template.ProjectID, "document": string(changed)}, 200)
	requireScopedStatus(t, request("changed-template", "third-db"), 403)
}

func TestScopedRuntimeCleanupCannotEditPolicyOrPruneHistory(t *testing.T) {
	a := serviceTestAPI(t)
	enableOperationsForTest(t, a)
	a.deploy.Retention = deploy.SimulationRetention{Store: a.store}
	projects, _ := a.store.ListProjects(context.Background())
	p := core.RetentionPolicy{ProjectID: projects[0].ID, LogDays: 30, RunDays: 90, KeepRuns: 5, ImageDays: 7, StoppedRevisionDays: 7, KeepRollbackRevisions: 2}
	path := "/api/v1/projects/" + p.ProjectID + "/retention"
	serviceRequestTest(t, a, "PUT", path, p, 200)
	token, _ := scopedServiceIdentity(t, a, p.ProjectID, core.PermissionProjectView, core.PermissionRuntimeCleanup)
	automationRequest(t, a, token, "GET", path, nil, 200)
	automationRequest(t, a, token, "PUT", path, p, 403)
	automationRequest(t, a, token, "POST", path+"/preview", map[string]any{"scope": "history"}, 403)
	automationRequest(t, a, token, "POST", path+"/apply", map[string]any{"scope": "history", "confirm": p.ProjectID}, 403)
	w := automationRequest(t, a, token, "POST", path+"/preview", map[string]any{"scope": "runtime", "expectedPolicy": p}, 200)
	var result core.RetentionResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Runtime == nil {
		t.Fatal("missing runtime review", err)
	}
	automationRequest(t, a, token, "GET", path+"/runtime-reviews/"+result.Runtime.ID, nil, 200)
	input := map[string]any{"scope": "runtime", "expectedPolicy": p, "runtimeReviewId": result.Runtime.ID, "runtimeReviewDigest": result.Runtime.Digest}
	automationRequest(t, a, token, "POST", path+"/apply", input, 400)
	input["confirm"] = p.ProjectID
	automationRequest(t, a, token, "POST", path+"/apply", input, 200)
	automationRequest(t, a, token, "GET", "/api/v1/projects/other-project/retention", nil, 403)
}

func TestScopedServiceProvisionRejectsScriptTemplates(t *testing.T) {
	a := serviceTestAPI(t)
	projects, _ := a.store.ListProjects(context.Background())
	var template core.SavedServiceTemplate
	raw := serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": projects[0].ID, "document": savedTemplateDocument}, 201)
	if err := json.Unmarshal(raw, &template); err != nil {
		t.Fatal(err)
	}
	token, _ := scopedServiceIdentity(t, a, template.ProjectID, core.PermissionProjectView, core.PermissionServiceProvision)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+template.ProjectID, map[string]any{"kind": "service_template", "resourceId": template.ID + "@" + template.Digest}, 422)
	// Even an old or externally written assignment cannot enable arbitrary execution.
	if err := a.store.(store.AutomationStore).SaveInfrastructureAssignment(context.Background(), core.InfrastructureAssignment{ProjectID: template.ProjectID, Kind: "service_template", ResourceID: template.ID + "@" + template.Digest, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	w := mutationRequest(a, token, "POST", "/api/v1/service-templates/"+template.ID+"/runs", "script-denied", map[string]any{"name": "unsafe-script"})
	requireScopedStatus(t, w, 403)
	runs, err := a.store.ListServiceProvisionRuns(context.Background(), template.ProjectID)
	if err != nil || len(runs) != 0 {
		t.Fatal("script execution was accepted", runs, err)
	}
}

func TestServiceQuotaOmittedUpdatePreservesConfiguredLimit(t *testing.T) {
	a := serviceTestAPI(t)
	projects, _ := a.store.ListProjects(context.Background())
	path := "/api/v1/projects/" + projects[0].ID + "/infrastructure/quota"
	automationRequest(t, a, "secret", "PUT", path, map[string]any{"maxServices": 3, "providers": []any{}}, 200)
	w := automationRequest(t, a, "secret", "PUT", path, map[string]any{"revision": 1, "maxServers": 4, "providers": []any{}}, 200)
	var policy core.InfrastructureQuotaPolicy
	if err := json.Unmarshal(w.Body.Bytes(), &policy); err != nil || policy.MaxServices != 3 {
		t.Fatal("omitted limit reset", policy, err)
	}
	automationRequest(t, a, "secret", "PUT", path, map[string]any{"revision": 2, "maxServices": -2, "providers": []any{}}, 422)
}

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

const savedTemplateDocument = `apiVersion: dispatch/v1alpha1
kind: ServiceTemplate
metadata:
  name: saved-test
spec:
  serviceType: generic
  inputs:
    endpoint:
      type: string
      required: true
    token:
      type: secret
      required: true
  provision:
    run: |
      printf 'endpoint=%s\ntoken=%s\n' "$DISPATCH_INPUT_ENDPOINT" "$DISPATCH_INPUT_TOKEN" > "$DISPATCH_OUTPUT_FILE"
  outputs:
    endpoint: {}
    token:
      sensitive: true
`

func TestSavedServiceTemplateLifecycleWithoutRepository(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	body := serviceTemplateRequest{ProjectID: projects[0].ID, Document: savedTemplateDocument}
	response := serviceRequestTest(t, a, "POST", "/api/v1/service-templates", body, 201)
	var item core.SavedServiceTemplate
	if err := json.Unmarshal(response, &item); err != nil {
		t.Fatal(err)
	}
	if item.ConfigSourceID != "" || item.Revision != 1 {
		t.Fatalf("saved: %+v", item)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates", body, 409)
	list := serviceRequestTest(t, a, "GET", "/api/v1/service-templates", nil, 200)
	if !strings.Contains(string(list), `"managedBy":"dispatch"`) || strings.Contains(string(list), `"document"`) {
		t.Fatalf("list: %s", list)
	}
	detail := serviceRequestTest(t, a, "GET", "/api/v1/service-templates/"+item.ID, nil, 200)
	var view serviceTemplateView
	json.Unmarshal(detail, &view)
	if view.Document == "" || view.Revision != 1 || view.ManagedBy != "dispatch" {
		t.Fatalf("detail: %+v", view)
	}
	body.Revision = 1
	body.Document = strings.Replace(savedTemplateDocument, "serviceType: generic", "description: Updated template\n  serviceType: generic", 1)
	serviceRequestTest(t, a, "PUT", "/api/v1/service-templates/"+item.ID, body, 200)
	serviceRequestTest(t, a, "PUT", "/api/v1/service-templates/"+item.ID, body, 409)
	serviceRequestTest(t, a, "DELETE", "/api/v1/service-templates/"+item.ID+"?revision=1", nil, 409)
	stored, err := a.store.GetSavedServiceTemplate(ctx, item.ID)
	if err != nil || stored.Revision != 2 || stored.Digest == item.Digest {
		t.Fatalf("update: %+v %v", stored, err)
	}
	response = serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+item.ID+"/runs", serviceProvisionRequest{Name: "saved-resource", Inputs: map[string]string{"endpoint": "https://example.test", "token": "private-test-value"}}, 202)
	var run core.ServiceProvisionRun
	json.Unmarshal(response, &run)
	deadline := time.Now().Add(5 * time.Second)
	for run.State == "queued" || run.State == "running" {
		if time.Now().After(deadline) {
			t.Fatal("provisioner did not finish")
		}
		time.Sleep(20 * time.Millisecond)
		run, err = a.store.GetServiceProvisionRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if run.State != "succeeded" {
		t.Fatalf("run: %+v", run)
	}
	service, err := a.store.GetService(ctx, run.ServiceID)
	if err != nil || service.TemplateID != item.ID || service.TemplateConfigSHA != stored.Digest || service.Fields["token"].Value != "" || service.Fields["token"].EncryptedValue == "" {
		t.Fatalf("service: %+v %v", service, err)
	}
	serviceRequestTest(t, a, "DELETE", "/api/v1/service-templates/"+item.ID+"?revision=2", nil, 204)
	serviceRequestTest(t, a, "GET", "/api/v1/service-templates/"+item.ID, nil, 404)
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+item.ID+"/runs", serviceProvisionRequest{Name: "deleted-template"}, 404)
	if _, err := a.store.GetService(ctx, run.ServiceID); err != nil {
		t.Fatal("deleting template removed its service", err)
	}
	if _, err := a.store.GetServiceProvisionRun(ctx, run.ID); err != nil {
		t.Fatal("deleting template removed its run", err)
	}
}

func TestSavedServiceTemplateValidationAndRepositoryOwnership(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0].ID
	for _, document := range []string{"", savedTemplateDocument + "---\n" + savedTemplateDocument, strings.Replace(savedTemplateDocument, "kind: ServiceTemplate", "kind: Pipeline", 1), strings.Replace(savedTemplateDocument, "type: string", "type: invalid", 1), strings.Replace(savedTemplateDocument, "serviceType: generic", "serviceType: postgresql", 1)} {
		serviceRequestTest(t, a, "POST", "/api/v1/service-templates", serviceTemplateRequest{ProjectID: project, Document: document}, 400)
	}
	now := time.Now().UTC()
	if err := a.store.CreateProject(ctx, core.Project{ID: "other-template-project", Name: "Other", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateSecret(ctx, core.Secret{ID: "saved-template-credential", Name: "saved template credential", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{project, "other-template-project"} {
		if err := a.store.CreateConfigSource(ctx, core.ConfigSource{ID: "config-" + p, ProjectID: p, Name: "config-" + p, CredentialSecretID: "saved-template-credential", Repository: "example/templates", Branch: "main", Path: "deployment/" + p, Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	document := strings.Replace(savedTemplateDocument, "  provision:", "  sources:\n    code:\n      repository: example/provisioner\n  provision:", 1)
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates", serviceTemplateRequest{ProjectID: project, Document: document}, 400)
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates", serviceTemplateRequest{ProjectID: project, ConfigSourceID: "config-other-template-project", Document: document}, 400)
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates", serviceTemplateRequest{ProjectID: project, ConfigSourceID: "config-" + project, Document: document}, 201)
	if err := a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "repository-template", ConfigSourceID: "config-" + project, Kind: "ServiceTemplate", Name: "gitops", Document: savedTemplateDocument, Active: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/service-templates/repository-template", serviceTemplateRequest{ProjectID: project, Document: savedTemplateDocument, Revision: 1}, 409)
	serviceRequestTest(t, a, "DELETE", "/api/v1/service-templates/repository-template?revision=1", nil, 409)
}

func TestSavedServiceTemplateProjectPermissions(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0].ID
	body := serviceTemplateRequest{ProjectID: project, Document: savedTemplateDocument}
	data := serviceRequestTest(t, a, "POST", "/api/v1/service-templates", body, 201)
	var item core.SavedServiceTemplate
	json.Unmarshal(data, &item)
	now := time.Now().UTC()
	user := core.User{ID: "template-viewer", Username: "template-viewer", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "template-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: project, Role: "viewer", CreatedAt: now, UpdatedAt: now}
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	call := func(handler http.HandlerFunc, path string, payload any, want int) []byte {
		t.Helper()
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", path, strings.NewReader(string(b)))
		req = req.WithContext(withIdentity(req.Context(), core.Identity{ID: user.ID, SystemRole: "member"}))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", item.ID)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code != want {
			t.Fatalf("status %d, want %d: %s", rr.Code, want, rr.Body.String())
		}
		return rr.Body.Bytes()
	}
	call(a.getServiceTemplate, "/", nil, 200)
	call(a.createServiceTemplate, "/", body, 403)
	body.Revision = 1
	call(a.updateServiceTemplate, "/", body, 403)
	call(a.deleteServiceTemplate, "/?revision=1", nil, 403)
	call(a.startServiceProvision, "/", serviceProvisionRequest{Name: "forbidden"}, 403)
	grant.Role = "operator"
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	body.ProjectID = "inaccessible"
	call(a.createServiceTemplate, "/", body, 403)
	call(a.updateServiceTemplate, "/", body, 400)
	// Removing access hides both summaries and full YAML.
	if err := a.store.DeleteRoleAssignment(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	call(a.getServiceTemplate, "/", nil, 403)
	list := call(a.listServiceTemplates, "/", nil, 200)
	if strings.Contains(string(list), item.ID) {
		t.Fatal("template listed across project boundary")
	}
	if _, err := a.store.GetSavedServiceTemplate(ctx, item.ID); err != nil {
		t.Fatal("denied delete removed template")
	}
}

func TestProjectDeletionRequiresRemovingSavedTemplates(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	if err := a.store.CreateProject(ctx, core.Project{ID: "template-project", Name: "Template project", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	response := serviceRequestTest(t, a, "POST", "/api/v1/service-templates", serviceTemplateRequest{ProjectID: "template-project", Document: savedTemplateDocument}, 201)
	var item core.SavedServiceTemplate
	if err := json.Unmarshal(response, &item); err != nil {
		t.Fatal(err)
	}
	problem := serviceRequestTest(t, a, "DELETE", "/api/v1/projects/template-project", nil, 409)
	if !strings.Contains(string(problem), "saved service templates") {
		t.Fatalf("conflict: %s", problem)
	}
	serviceRequestTest(t, a, "DELETE", "/api/v1/service-templates/"+item.ID+"?revision=1", nil, 204)
	serviceRequestTest(t, a, "DELETE", "/api/v1/projects/template-project", nil, 204)
}

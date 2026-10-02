package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/neon"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
)

type neonAPIFixture struct {
	mu               sync.Mutex
	branch           *neon.Branch
	marks            map[string]string
	role             string
	creates, deletes int
	loseCreate       bool
}

func (f *neonAPIFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	send := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	if r.Header.Get("Authorization") != "Bearer scoped-neon-key" {
		w.WriteHeader(401)
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v2/projects/neon-project")
	switch {
	case p == "/branches" && r.Method == "GET":
		branches := []neon.Branch{}
		annotations := map[string]any{}
		if f.branch != nil {
			branches = append(branches, *f.branch)
			annotations[f.branch.ID] = map[string]any{"value": f.marks}
		}
		send(map[string]any{"branches": branches, "annotations": annotations})
	case p == "/branches/parent" && r.Method == "GET":
		send(map[string]any{"branch": neon.Branch{ID: "parent", ProjectID: "neon-project", Default: true, Protected: true}})
	case strings.HasPrefix(p, "/branches/parent/roles/"):
		w.WriteHeader(404)
	case p == "/branches" && r.Method == "POST":
		var body struct {
			Branch struct {
				Name, InitSource string `json:"-"`
			}
			Annotations map[string]string `json:"annotation_value"`
		}
		var raw map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&raw)
		var branch map[string]string
		json.Unmarshal(raw["branch"], &branch)
		json.Unmarshal(raw["annotation_value"], &body.Annotations)
		f.branch = &neon.Branch{ID: "owned", ProjectID: "neon-project", Name: branch["name"], InitSource: branch["init_source"], State: "ready"}
		f.marks = body.Annotations
		f.creates++
		if f.loseCreate {
			f.loseCreate = false
			w.WriteHeader(502)
			fmt.Fprint(w, "scoped-neon-key private response")
			return
		}
		send(map[string]any{"branch": f.branch})
	case p == "/branches/owned" && r.Method == "GET":
		if f.branch == nil {
			w.WriteHeader(404)
		} else {
			send(map[string]any{"branch": f.branch, "annotation": map[string]any{"value": f.marks}})
		}
	case p == "/branches/owned" && r.Method == "DELETE":
		f.branch = nil
		f.deletes++
		w.WriteHeader(204)
	case p == "/branches/owned/endpoints":
		send(map[string]any{"endpoints": []neon.Endpoint{{ID: "compute", BranchID: "owned", ProjectID: "neon-project", Type: "read_write", Host: "compute.example.test", State: "active"}}})
	case strings.HasPrefix(p, "/branches/owned/roles/") && r.Method == "GET":
		if f.role == "" {
			w.WriteHeader(404)
		} else {
			send(map[string]any{"role": map[string]any{"name": f.role, "branch_id": "owned"}})
		}
	case p == "/branches/owned/roles" && r.Method == "POST":
		var body struct {
			Role struct {
				Name string `json:"name"`
			} `json:"role"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.role = body.Role.Name
		send(map[string]any{"role": map[string]any{"name": f.role, "branch_id": "owned"}})
	case p == "/connection_uri":
		uri := url.URL{Scheme: "postgresql", Host: "compute.example.test", Path: "/neondb", User: url.UserPassword(f.role, "child-only-password"), RawQuery: "sslmode=require"}
		send(map[string]any{"uri": uri.String()})
	default:
		w.WriteHeader(404)
	}
}
func neonServiceFixture(t *testing.T, lose bool) (*API, *neonAPIFixture, core.NeonProvider, core.SavedServiceTemplate) {
	t.Helper()
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	cipher, err := a.eventConfig.Vault.Encrypt("secret:neon-credential", []byte("scoped-neon-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.CreateSecret(ctx, core.Secret{ID: "neon-credential", Name: "Neon", EncryptedValue: cipher, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f := &neonAPIFixture{loseCreate: lose}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	a.neonHTTPClient = server.Client()
	var provider core.NeonProvider
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/neon-providers", map[string]any{"projectId": projects[0].ID, "name": "Preview databases", "endpoint": server.URL + "/api/v2", "neonProjectId": "neon-project", "parentBranchId": "parent", "credentialRef": "neon-credential"}, 201), &provider)
	doc := fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata: {name: neon-preview}\nspec:\n  serviceType: postgresql\n  provision:\n    neon: {providerRef: %s, database: neondb}\n", provider.ID)
	var template core.SavedServiceTemplate
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": provider.ProjectID, "document": doc}, 201), &template)
	return a, f, provider, template
}
func TestNeonServiceDurableRecoveryEncryptedBindingAndReviewedDataDeletion(t *testing.T) {
	a, f, _, template := neonServiceFixture(t, true)
	w := mutationRequest(a, "secret", "POST", "/api/v1/service-templates/"+template.ID+"/runs", "neon-create", map[string]any{"name": "pr-database"})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	receipt := decodeMutation(t, w)
	record := awaitServiceResource(t, a, receipt.OperationID, "unresolved")
	if !record.ProviderCreateAttempted {
		t.Fatal("missing durable create fence")
	}
	if strings.Contains(record.EncryptedRequest, "scoped-neon-key") {
		t.Fatal("plaintext provider credential")
	}
	path := "/api/v1/service-provision-runs/" + record.RunID + "/resource"
	serviceRequestTest(t, a, "POST", path+"/reconcile", nil, 202)
	record = awaitServiceResource(t, a, record.RunID, "ready")
	service, err := a.store.GetService(context.Background(), record.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := (serviceconn.Resolver{Vault: a.eventConfig.Vault}).Resolve(context.Background(), service)
	if err != nil || outputs["password"] != "child-only-password" {
		t.Fatal(outputs, err)
	}
	raw := serviceRequestTest(t, a, "GET", path, nil, 200)
	if strings.Contains(string(raw), "scoped-neon-key") || strings.Contains(string(raw), "child-only-password") {
		t.Fatal("public result leaked credentials")
	}
	f.mu.Lock()
	created, mode := f.creates, f.branch.InitSource
	f.mu.Unlock()
	if created != 1 || mode != "schema-only" {
		t.Fatal("unsafe or repeated create", created, mode)
	}
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", path+"/delete-preview", nil, 200), &review)
	if review.BlockedReason != "" || review.StoragePolicy != "delete" || !strings.Contains(review.Summary, "all branch data") {
		t.Fatal("deletion concealed data loss", review)
	}
	input := map[string]any{"confirmation": destructiveConfirmation{ResourceID: review.ResourceID, Action: review.Action, ExpectedVersion: review.Version, ConfirmName: review.Name}}
	serviceRequestTest(t, a, "POST", path+"/delete", input, 202)
	awaitServiceResource(t, a, record.RunID, "deleted")
	if _, err = a.store.GetService(context.Background(), record.ServiceID); err != store.ErrNotFound {
		t.Fatal("deleted branch still bound", err)
	}
}

func TestNeonPreviewServicesReuseAfterNewRevisionAndProtectActivePreview(t *testing.T) {
	a, f, provider, template := neonServiceFixture(t, false)
	ctx := context.Background()
	now := time.Now().UTC()
	source := core.ConfigSource{ID: "preview-config", CredentialSecretID: "neon-credential", ProjectID: provider.ProjectID, Name: "Preview source", Repository: "example/app", Active: true, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	preview := core.WorkflowResource{ID: "preview-resource", ConfigSourceID: source.ID, Name: "Pull request 12", Kind: workflow.KindApplication, Temporary: true, Active: true, State: "ready", Path: "preview.yaml", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	document := workflow.Document{Spec: &workflow.ApplicationSpec{PreviewServices: map[string]workflow.PreviewServiceSpec{"database": {TemplateRef: template.ID}}}}
	scope := "preview-service:database:" + template.ID + ":" + template.Digest + ":" + provider.ID
	revision := core.WorkflowRevision{ID: "revision-one", ResourceID: preview.ID, SourceTrust: &core.PreviewSourceTrustDecision{Allowed: false, CredentialScope: []string{scope}}}
	if err := a.prepareNeonPreviewServices(ctx, preview, revision, document); err == nil {
		t.Fatal("unapproved preview provisioned")
	}
	f.mu.Lock()
	creates := f.creates
	f.mu.Unlock()
	if creates != 0 {
		t.Fatal("trust gate ran after provider mutation")
	}
	revision.SourceTrust.Allowed = true
	if err := a.prepareNeonPreviewServices(ctx, preview, revision, document); err != nil {
		t.Fatal(err)
	}
	runID, err := a.store.(store.NeonStore).GetNeonPreviewService(ctx, preview.ID, "database")
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	revision.ID = "revision-two"
	if err = a.prepareNeonPreviewServices(ctx, preview, revision, document); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	creates = f.creates
	f.mu.Unlock()
	if creates != 1 {
		t.Fatal("new commit duplicated preview database", creates)
	}
	busy, err := a.store.(store.ServiceResourceStore).ServiceResourceConsumers(ctx, record.ServiceID)
	if err != nil || !busy {
		t.Fatal("active preview did not protect its database", busy, err)
	}
	preview.Active = false
	preview.State = "expired"
	if err = a.store.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	busy, err = a.store.(store.ServiceResourceStore).ServiceResourceConsumers(ctx, record.ServiceID)
	if err != nil || busy {
		t.Fatal("closed preview still hides reviewed data deletion", busy, err)
	}
	f.mu.Lock()
	exists := f.branch != nil
	f.mu.Unlock()
	if !exists {
		t.Fatal("expiry implicitly deleted data")
	}
}
func TestNeonProjectProviderAssignmentAndDataCopyApproval(t *testing.T) {
	a, _, provider, template := neonServiceFixture(t, false)
	ctx := context.Background()
	if err := a.store.CreateProject(ctx, core.Project{ID: "foreign-neon", Name: "Other", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	doc := strings.Replace(template.Document, "name: neon-preview", "name: other", 1)
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": "foreign-neon", "document": doc}, 400)
	docs, err := workflow.Parse("template.yaml", []byte(template.Document))
	if err != nil {
		t.Fatal(err)
	}
	docs[0].ServiceTemplate.Provision.Neon.DataMode = "parent-data"
	raw, err := docs[0].MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	var copied core.SavedServiceTemplate
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": provider.ProjectID, "document": strings.Replace(string(raw), "name: neon-preview", "name: approved-copy", 1)}, 201), &copied)
	serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+copied.ID+"/runs", map[string]any{"name": "copy-without-confirmation"}, 403)
}

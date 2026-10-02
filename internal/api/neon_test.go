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

type neonArchivedBranch struct {
	Branch neon.Branch
	Marks  map[string]string
	Role   string
}
type neonAPIFixture struct {
	archived         []neonArchivedBranch
	suspendCalls     int
	suspended        bool
	loseDelete       bool
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

	currentID := "owned"
	if f.branch != nil {
		currentID = f.branch.ID
	}
	for _, old := range f.archived {
		if p == "/branches/"+old.Branch.ID+"/endpoints" {
			send(map[string]any{"endpoints": []neon.Endpoint{{ID: "compute", BranchID: old.Branch.ID, ProjectID: "neon-project", Type: "read_write", Host: "compute.example.test", State: "active"}}})
			return
		}
		if strings.HasPrefix(p, "/branches/"+old.Branch.ID+"/roles/") {
			send(map[string]any{"role": map[string]any{"name": old.Role, "branch_id": old.Branch.ID}})
			return
		}
		if p == "/branches/"+old.Branch.ID && r.Method == "GET" {
			send(map[string]any{"branch": old.Branch, "annotation": map[string]any{"value": old.Marks}})
			return
		}
	}
	if currentID != "owned" {
		p = strings.Replace(p, "/branches/"+currentID, "/branches/owned", 1)
	}
	switch {
	case p == "/branches" && r.Method == "GET":
		branches := []neon.Branch{}
		annotations := map[string]any{}
		for _, old := range f.archived {
			branches = append(branches, old.Branch)
			annotations[old.Branch.ID] = map[string]any{"value": old.Marks}
		}
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
		if f.branch != nil {
			f.archived = append(f.archived, neonArchivedBranch{*f.branch, f.marks, f.role})
		}
		branchID := "owned"
		if f.creates > 0 {
			branchID = fmt.Sprintf("owned-%d", f.creates+1)
		}
		f.role = ""
		f.branch = &neon.Branch{ID: branchID, ProjectID: "neon-project", Name: branch["name"], InitSource: branch["init_source"], State: "ready"}
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
		f.deletes++
		if f.loseDelete {
			f.loseDelete = false
			w.WriteHeader(502)
			return
		}
		f.branch = nil
		w.WriteHeader(204)
	case p == "/endpoints/compute/suspend":
		f.suspendCalls++
		f.suspended = true
		send(map[string]any{})
	case p == "/branches/owned/endpoints":
		state := "active"
		if f.suspended {
			state = "idle"
		}
		send(map[string]any{"endpoints": []neon.Endpoint{{ID: "compute", BranchID: currentID, ProjectID: "neon-project", Type: "read_write", Host: "compute.example.test", State: state}}})
	case strings.HasPrefix(p, "/branches/owned/roles/") && r.Method == "GET":
		if f.role == "" {
			w.WriteHeader(404)
		} else {
			send(map[string]any{"role": map[string]any{"name": f.role, "branch_id": currentID}})
		}
	case p == "/branches/owned/roles" && r.Method == "POST":
		var body struct {
			Role struct {
				Name string `json:"name"`
			} `json:"role"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.role = body.Role.Name
		send(map[string]any{"role": map[string]any{"name": f.role, "branch_id": currentID}})
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

func createNeonPreviewForTest(t *testing.T, a *API, provider core.NeonProvider, template core.SavedServiceTemplate) (core.WorkflowResource, core.ServiceResource) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	source := core.ConfigSource{ID: "policy-config", CredentialSecretID: "neon-credential", ProjectID: provider.ProjectID, Name: "Policy source", Repository: "example/app", Active: true, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	preview := core.WorkflowResource{ID: "policy-preview", ConfigSourceID: source.ID, Name: "Policy preview", Kind: workflow.KindApplication, Temporary: true, Active: true, State: "ready", Path: "preview.yaml", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	document := workflow.Document{Spec: &workflow.ApplicationSpec{PreviewServices: map[string]workflow.PreviewServiceSpec{"database": {TemplateRef: template.ID}}}}
	if err := a.prepareNeonPreviewServices(ctx, preview, core.WorkflowRevision{}, document); err != nil {
		t.Fatal(err)
	}
	run, err := a.store.(store.NeonStore).GetNeonPreviewService(ctx, preview.ID, "database")
	if err != nil {
		t.Fatal(err)
	}
	record, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	return preview, record
}
func setNeonPolicyForTest(t *testing.T, a *API, record core.ServiceResource, policy string) {
	t.Helper()
	path := "/api/v1/service-provision-runs/" + record.RunID + "/resource/policy-" + policy
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", path+"-preview", nil, 200), &review)
	if review.BlockedReason != "" {
		t.Fatal(review.BlockedReason)
	}
	if policy == "delete" && !strings.Contains(review.Summary, "all of its data") {
		t.Fatal("cleanup policy concealed data loss")
	}
	input := map[string]any{"confirmation": destructiveConfirmation{ResourceID: review.ResourceID, Action: review.Action, ExpectedVersion: review.Version, ConfirmName: review.Name}}
	serviceRequestTest(t, a, "POST", path, input, 200)
}

func TestNeonResetPreservesOldBindingUntilRecoveredSchemaOnlyGenerationCommits(t *testing.T) {
	a, f, provider, template := neonServiceFixture(t, false)
	preview, old := createNeonPreviewForTest(t, a, provider, template)
	path := "/api/v1/service-provision-runs/" + old.RunID + "/resource/reset"
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", path+"-preview", nil, 200), &review)
	if review.BlockedReason == "" {
		t.Fatal("active preview allowed reset")
	}
	ctx := context.Background()
	preview.Active = false
	if err := a.store.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	review = destructiveReview{}
	json.Unmarshal(serviceRequestTest(t, a, "POST", path+"-preview", nil, 200), &review)
	if review.BlockedReason != "" || !strings.Contains(review.Summary, "Parent rows are never copied") {
		t.Fatal(review)
	}
	f.mu.Lock()
	f.loseCreate = true
	f.mu.Unlock()
	input := map[string]any{"confirmation": destructiveConfirmation{ResourceID: review.ResourceID, Action: review.Action, ExpectedVersion: review.Version, ConfirmName: review.Name}}
	w := mutationRequest(a, "secret", "POST", path, "reviewed-reset", input)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	receipt := decodeMutation(t, w)
	candidate := awaitServiceResource(t, a, receipt.OperationID, "unresolved")
	replay := mutationRequest(a, "secret", "POST", path, "reviewed-reset", input)
	if replay.Code != 202 || decodeMutation(t, replay).OperationID != candidate.RunID {
		t.Fatal("reset replay lost accepted identity", replay.Code, replay.Body.String())
	}
	linked, _ := a.store.(store.NeonStore).GetNeonPreviewService(ctx, preview.ID, "database")
	if linked != old.RunID || candidate.ReplacesRunID != old.RunID {
		t.Fatal("failed reset changed original binding", linked, candidate)
	}
	if _, err := a.store.GetService(ctx, old.ServiceID); err != nil {
		t.Fatal("old connection removed", err)
	}
	preview.Active = true
	if err := a.store.UpdateWorkflowResource(ctx, preview); err == nil {
		t.Fatal("pending replacement allowed preview resume")
	}
	if _, err := a.store.BeginWorkflowPreviewCleanup(ctx, preview.ID, "closed", time.Now()); err == nil {
		t.Fatal("pending replacement escaped cleanup inventory")
	}
	// A second accepted reset must not create another candidate.
	serviceRequestTest(t, a, "POST", path, input, 409)
	serviceRequestTest(t, a, "POST", "/api/v1/service-provision-runs/"+candidate.RunID+"/resource/reconcile", nil, 202)
	candidate = awaitServiceResource(t, a, candidate.RunID, "ready")
	linked, _ = a.store.(store.NeonStore).GetNeonPreviewService(ctx, preview.ID, "database")
	if linked != candidate.RunID || candidate.ServiceID == old.ServiceID || candidate.ResourceID == old.ResourceID {
		t.Fatal("replacement identity was reused", linked, candidate)
	}
	if _, err := a.store.GetService(ctx, old.ServiceID); err != nil {
		t.Fatal("old connection lost on successful reset", err)
	}
	accepted, _, err := a.loadAcceptedServiceResource(ctx, candidate)
	if err != nil || accepted.Neon.Scope.Generation != 2 || accepted.Neon.Spec.DataMode == "parent-data" {
		t.Fatal("unsafe reset", accepted.Neon, err)
	}
	f.mu.Lock()
	creates, deletes, retained := f.creates, f.deletes, len(f.archived)
	mode := f.branch.InitSource
	f.mu.Unlock()
	if creates != 2 || deletes != 0 || retained != 1 || mode != "schema-only" {
		t.Fatal("reset mutated or copied old data", creates, deletes, retained, mode)
	}
	if err := a.store.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal("committed replacement did not release preview", err)
	}
}
func TestNeonPreviewCleanupUsesReviewedPolicyAndPreservesUnknownDelete(t *testing.T) {
	for _, policy := range []string{"retain", "suspend", "delete"} {
		t.Run(policy, func(t *testing.T) {
			a, f, provider, template := neonServiceFixture(t, false)
			preview, record := createNeonPreviewForTest(t, a, provider, template)
			if policy != "retain" {
				setNeonPolicyForTest(t, a, record, policy)
			}
			f.mu.Lock()
			f.loseDelete = policy == "delete"
			f.mu.Unlock()
			ctx := context.Background()
			cleanup, err := a.store.BeginWorkflowPreviewCleanup(ctx, preview.ID, "closed", time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			err = a.reconcileWorkflowPreviewCleanup(ctx, cleanup.ID)
			if policy == "delete" {
				if err == nil {
					t.Fatal("lost deletion was reported as success")
				}
				if err = a.reconcileWorkflowPreviewCleanup(ctx, cleanup.ID); err == nil {
					t.Fatal("unknown deletion should stay blocked")
				}
				f.mu.Lock()
				f.branch.Name = "renamed-but-still-present"
				f.mu.Unlock()
				if err = a.reconcileWorkflowPreviewCleanup(ctx, cleanup.ID); err == nil {
					t.Fatal("renamed branch treated as deleted")
				}
				serviceRequestTest(t, a, "POST", "/api/v1/service-provision-runs/"+record.RunID+"/resource/reconcile", nil, 409)
				f.mu.Lock()
				calls := f.deletes
				f.branch = nil
				f.mu.Unlock()
				if calls != 1 {
					t.Fatal("unknown delete was repeated", calls)
				}
				if err = a.reconcileWorkflowPreviewCleanup(ctx, cleanup.ID); err != nil {
					t.Fatal("observed provider completion did not converge", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			results, err := a.store.ListWorkflowPreviewCleanups(ctx, preview.ID)
			if err != nil || len(results) != 1 || results[0].State != "succeeded" || len(results[0].Services) != 1 || results[0].Services[0].Policy != policy {
				t.Fatal("missing durable service cleanup", results, err)
			}
			f.mu.Lock()
			exists, calls, suspends := f.branch != nil, f.deletes, f.suspendCalls
			f.mu.Unlock()
			if policy == "retain" && (!exists || calls != 0 || suspends != 0) {
				t.Fatal("retain policy mutated provider")
			}
			if policy == "suspend" && (!exists || calls != 0 || suspends != 1) {
				t.Fatal("suspend policy removed data or duplicated mutation")
			}
		})
	}
}

func TestNeonCleanupCheckpointNeverClearsAndSuspendProtectsSharedConsumers(t *testing.T) {
	a, _, provider, template := neonServiceFixture(t, false)
	ctx := context.Background()
	preview, record := createNeonPreviewForTest(t, a, provider, template)
	setNeonPolicyForTest(t, a, record, "suspend")
	server := core.Server{ID: "shared-target", ProjectID: provider.ProjectID, Name: "shared", Runtime: "docker", Address: "local", CreatedAt: time.Now()}
	if err := a.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "other-consumer", Name: "other", ProjectID: provider.ProjectID, ServerID: server.ID, BuildType: core.BuildTypeDockerfile, CreatedAt: time.Now()}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	binding := []core.ServiceBinding{{Alias: "db", ServiceRef: record.ServiceID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}
	if err := a.store.ReplaceAppServiceBindings(ctx, app.ID, binding); err != nil {
		t.Fatal(err)
	}
	c, err := a.store.BeginWorkflowPreviewCleanup(ctx, preview.ID, "closed", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := a.store.ClaimWorkflowPreviewCleanup(ctx, c.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	entry := claim.Services[0]
	data := a.store.(store.NeonLifecycleStore)
	if _, err = data.ClaimNeonCleanupService(ctx, *claim, entry); err == nil {
		t.Fatal("shared consumer allowed suspension")
	}
	if err = a.store.ReplaceAppServiceBindings(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = data.ClaimNeonCleanupService(ctx, *claim, entry); err != nil {
		t.Fatal(err)
	}
	if err = a.store.ReplaceAppServiceBindings(ctx, app.ID, binding); err == nil {
		t.Fatal("late consumer raced suspension")
	}
	entry.State = "blocked"
	entry.ActionStarted = false
	if err = data.SaveNeonCleanupService(ctx, *claim, entry); err != nil {
		t.Fatal(err)
	}
	saved, err := a.store.ListWorkflowPreviewCleanups(ctx, preview.ID)
	if err != nil || !saved[0].Services[0].ActionStarted {
		t.Fatal("stale checkpoint reopened provider mutation", saved, err)
	}
}
func TestNeonProviderRemovalProtectsTemplateAndRecoveryReferences(t *testing.T) {
	a, _, provider, _ := neonServiceFixture(t, false)
	serviceRequestTest(t, a, "DELETE", "/api/v1/neon-providers/"+provider.ID, nil, 409)
	var unused core.NeonProvider
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/neon-providers", map[string]any{"projectId": provider.ProjectID, "name": "unused", "neonProjectId": "other", "parentBranchId": "parent", "credentialRef": "neon-credential"}, 201), &unused)
	serviceRequestTest(t, a, "DELETE", "/api/v1/neon-providers/"+unused.ID, nil, 204)
}

func TestNeonResetUnstartedCandidateCanBeCancelledWithoutProviderMutation(t *testing.T) {
	a, f, provider, template := neonServiceFixture(t, false)
	ctx := context.Background()
	preview, old := createNeonPreviewForTest(t, a, provider, template)
	preview.Active = false
	if err := a.store.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	resource, spec, err := a.neonResetTemplate(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	target, err := a.serviceProvisionTarget(ctx, spec, provider.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	run := core.ServiceProvisionRun{ID: "unstarted-reset", ProjectID: provider.ProjectID, TemplateID: template.ID, Target: target, ServiceName: "unstarted-reset", State: "queued", CreatedAt: time.Now()}
	accepted := context.WithValue(ctx, neonPreviewAcceptanceKey{}, neonPreviewAcceptance{ID: preview.ID, Alias: "database", ReplacesRunID: old.RunID, ReplacesRevision: old.Revision, Generation: 2})
	if err = a.captureServiceResource(accepted, resource, spec, "", nil, run, nil); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/service-provision-runs/" + run.ID + "/resource/delete"
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", path+"-preview", nil, 200), &review)
	if !strings.Contains(review.Summary, "no provider mutation") || review.BlockedReason != "" {
		t.Fatal(review)
	}
	input := map[string]any{"confirmation": destructiveConfirmation{ResourceID: review.ResourceID, Action: review.Action, ExpectedVersion: review.Version, ConfirmName: review.Name}}
	serviceRequestTest(t, a, "POST", path, input, 202)
	awaitServiceResource(t, a, run.ID, "deleted")
	linked, _ := a.store.(store.NeonStore).GetNeonPreviewService(ctx, preview.ID, "database")
	if linked != old.RunID {
		t.Fatal("cancellation changed original binding")
	}
	preview.Active = true
	if err = a.store.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal("cancelled replacement blocked resume", err)
	}
	f.mu.Lock()
	creates, deletes := f.creates, f.deletes
	f.mu.Unlock()
	if creates != 1 || deletes != 0 {
		t.Fatal("unstarted cancellation mutated provider", creates, deletes)
	}
}

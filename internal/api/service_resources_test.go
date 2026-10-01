package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
)

type resourceRuntimeFixture struct {
	mu               sync.Mutex
	exists           bool
	created, deleted int
	password         string
	failDelete       bool
	wrongOwner       bool
}

func (f *resourceRuntimeFixture) Provision(_ context.Context, r acceptedServiceResource, s core.Server) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.exists {
		f.created++
		f.exists = true
		f.password = r.Request.Password
	}
	return deploy.ServiceResourceOutputs(r.Request, r.Docker, r.Helm)
}
func (f *resourceRuntimeFixture) Inspect(_ context.Context, r acceptedServiceResource, s core.Server) (core.ServiceResourceInspection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := core.ServiceResourceInspection{RunID: r.Request.Run.ID, ProjectID: r.Request.Run.ProjectID, ServerID: s.ID, Provider: "docker", State: "absent", StorageRetained: true}
	if f.exists {
		result.State = "ready"
		result.ResourceID = "immutable-owned-container"
	}
	if f.wrongOwner {
		result.ProjectID = "other-project"
	}
	return result, nil
}
func (f *resourceRuntimeFixture) Delete(_ context.Context, r acceptedServiceResource, s core.Server, expected, operation string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.exists {
		if expected != "immutable-owned-container" {
			return errors.New("changed identity")
		}
		f.exists = false
		f.deleted++
	}
	if f.failDelete {
		f.failDelete = false
		return errors.New("lost cleanup reply")
	}
	return nil
}

type resourceFailingBindingStore struct {
	store.Store
	store.ServiceResourceStore
	store.MutationReceiptStore
	failed bool
}

func (s *resourceFailingBindingStore) SaveServiceResource(ctx context.Context, r core.ServiceResource, run core.ServiceProvisionRun, item *core.Service) error {
	if item != nil && !s.failed {
		s.failed = true
		return errors.New("simulated connection persistence failure")
	}
	return s.ServiceResourceStore.SaveServiceResource(ctx, r, run, item)
}
func resourceAPIFixture(t *testing.T) (*API, *resourceRuntimeFixture, core.SavedServiceTemplate) {
	t.Helper()
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	server := core.Server{ID: "resource-target", Name: "Resource target", Runtime: "docker", Address: "local", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	runtime := &resourceRuntimeFixture{}
	a.serviceResourceBackend = runtime
	a.deploy.Storage.Backend = &storageTestBackend{absent: true}
	doc := fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata: {name: recovery}\nspec:\n  serviceType: postgresql\n  provision:\n    docker: {serverRef: %s}\n", server.ID)
	var template core.SavedServiceTemplate
	if err := json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates", map[string]any{"projectId": projects[0].ID, "document": doc}, 201), &template); err != nil {
		t.Fatal(err)
	}
	return a, runtime, template
}
func awaitServiceResource(t *testing.T, a *API, id, state string) core.ServiceResource {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var record core.ServiceResource
	for time.Now().Before(deadline) {
		record, _ = a.store.(store.ServiceResourceStore).GetServiceResource(context.Background(), id)
		if record.State == state {
			return record
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("resource did not become %s: %#v", state, record)
	return record
}
func TestServiceResourceRecoversBindingFailureAndInterruptedDeletion(t *testing.T) {
	a, runtime, template := resourceAPIFixture(t)
	ctx := context.Background()
	data := a.store
	a.store = &resourceFailingBindingStore{Store: data, ServiceResourceStore: data.(store.ServiceResourceStore), MutationReceiptStore: data.(store.MutationReceiptStore)}
	path := "/api/v1/service-templates/" + template.ID + "/runs"
	w := mutationRequest(a, "secret", "POST", path, "owned-service-create", map[string]any{"name": "recover-db"})
	if w.Code != 202 {
		t.Fatalf("acceptance: %d %s", w.Code, w.Body.String())
	}
	receipt := decodeMutation(t, w)
	record := awaitServiceResource(t, a, receipt.OperationID, "unresolved")
	if _, err := a.store.GetService(ctx, record.ServiceID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed binding unexpectedly saved", err)
	}
	if strings.Contains(record.EncryptedRequest, runtime.password) || record.EncryptedOutputs == "" {
		t.Fatal("missing encrypted recovery evidence")
	}
	resourcePath := "/api/v1/service-provision-runs/" + record.RunID + "/resource"
	raw := serviceRequestTest(t, a, "GET", resourcePath, nil, 200)
	if strings.Contains(string(raw), runtime.password) || strings.Contains(string(raw), "encrypted") {
		t.Fatal("public resource exposed private recovery material")
	}
	// The accepted template can be removed while its owned resource is recoverable.
	serviceRequestTest(t, a, "DELETE", fmt.Sprintf("/api/v1/service-templates/%s?revision=%d", template.ID, template.Revision), nil, 204)
	serviceRequestTest(t, a, "POST", resourcePath+"/reconcile", nil, 202)
	record = awaitServiceResource(t, a, record.RunID, "ready")
	item, err := a.store.GetService(ctx, record.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	values, err := (serviceconn.Resolver{Vault: a.eventConfig.Vault}).Resolve(ctx, item)
	if err != nil {
		t.Fatal(err)
	}
	runtime.mu.Lock()
	created, password := runtime.created, runtime.password
	runtime.mu.Unlock()
	if created != 1 || values["password"] != password {
		t.Fatal("recovery duplicated the resource or changed its credentials")
	}
	app := core.App{ID: "resource-consumer", ProjectID: record.ProjectID, ServerID: record.Target.ServerID, Name: "Consumer", BuildType: core.BuildTypeDockerfile, CreatedAt: time.Now().UTC()}
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	bindings := []core.ServiceBinding{{Alias: "db", ServiceRef: item.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}
	if err = a.store.ReplaceAppServiceBindings(ctx, app.ID, bindings); err != nil {
		t.Fatal(err)
	}
	var review destructiveReview
	json.Unmarshal(serviceRequestTest(t, a, "POST", resourcePath+"/delete-preview", nil, 200), &review)
	if review.BlockedReason == "" {
		t.Fatal("consumer disappeared from deletion review")
	}
	if err = a.store.ReplaceAppServiceBindings(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(serviceRequestTest(t, a, "POST", resourcePath+"/delete-preview", nil, 200), &review)
	input := map[string]any{"confirmation": destructiveConfirmation{ResourceID: review.ResourceID, Action: review.Action, ExpectedVersion: review.Version, ConfirmName: review.Name}}
	runtime.mu.Lock()
	runtime.failDelete = true
	runtime.mu.Unlock()
	w = mutationRequest(a, "secret", "POST", resourcePath+"/delete", "owned-service-delete", input)
	if w.Code != 202 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	deletion := decodeMutation(t, w)
	record = awaitServiceResource(t, a, record.RunID, "unresolved")
	serviceRequestTest(t, a, "POST", resourcePath+"/reconcile", nil, 202)
	record = awaitServiceResource(t, a, record.RunID, "deleted")
	if _, err = a.store.GetService(ctx, item.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted resource left a live registration")
	}
	w = mutationRequest(a, "secret", "POST", resourcePath+"/delete", "owned-service-delete", input)
	if w.Code != 202 || decodeMutation(t, w).OperationID != deletion.OperationID || decodeMutation(t, w).State != "succeeded" {
		t.Fatalf("delete receipt failed to converge: %d %s", w.Code, w.Body.String())
	}
	runtime.mu.Lock()
	deleted := runtime.deleted
	runtime.mu.Unlock()
	if deleted != 1 {
		t.Fatal("uncertain cleanup repeated deletion", deleted)
	}
	if record.EncryptedRequest == "" || record.EncryptedOutputs == "" {
		t.Fatal("deletion lost retained resource credentials")
	}
}

func TestServiceResourceRejectsMismatchedRecoveryOwnership(t *testing.T) {
	a, runtime, template := resourceAPIFixture(t)
	runtime.wrongOwner = true
	var run core.ServiceProvisionRun
	json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/service-templates/"+template.ID+"/runs", map[string]any{"name": "wrong-owner"}, 202), &run)
	awaitServiceResource(t, a, run.ID, "unresolved")
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.created != 0 {
		t.Fatal("ownership mismatch created another resource")
	}
}

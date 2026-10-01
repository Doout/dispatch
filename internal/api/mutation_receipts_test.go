package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
)

func mutationApp(t *testing.T, a *API) core.App {
	t.Helper()
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	servers, _ := a.store.ListServers(ctx)
	app := core.App{ID: "receipt-app", ProjectID: projects[0].ID, ServerID: servers[0].ID, Name: "Receipt app", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", State: "idle", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	return app
}
func mutationRequest(a *API, token, method, path, key string, body any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func decodeMutation(t *testing.T, w *httptest.ResponseRecorder) mutationReceiptResponse {
	t.Helper()
	var result mutationReceiptResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func awaitMutationDeployment(t *testing.T, a *API, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, err := a.store.GetDeployment(context.Background(), id)
		if err == nil && d.State.Terminal() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("accepted deployment did not finish")
}

func TestMutationReceiptConcurrentRetryCompletionAndAudit(t *testing.T) {
	a := serviceTestAPI(t)
	app := mutationApp(t, a)
	path := "/api/v1/apps/" + app.ID + "/deployments"
	key := "retry-deployment-001"
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- mutationRequest(a, "secret", "POST", path, key, map[string]any{})
		}()
	}
	wg.Wait()
	close(responses)
	receiptID, operationID := "", ""
	for w := range responses {
		if w.Code != 202 {
			t.Fatalf("concurrent request: %d %s", w.Code, w.Body.String())
		}
		result := decodeMutation(t, w)
		if receiptID == "" {
			receiptID, operationID = result.ID, result.OperationID
		}
		if result.ID != receiptID || result.OperationID != operationID || operationID == "" {
			t.Fatal("concurrent requests did not share operation")
		}
		if w.Header().Get("Location") != "/api/v1/mutation-receipts/"+receiptID {
			t.Fatal("receipt inspection reference missing")
		}
	}
	awaitMutationDeployment(t, a, operationID)
	w := mutationRequest(a, "secret", "POST", path, key, map[string]any{})
	result := decodeMutation(t, w)
	if w.Code != 202 || result.OperationID != operationID || result.State != "succeeded" || w.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("completed replay: %d %s", w.Code, w.Body.String())
	}
	w = mutationRequest(a, "secret", "POST", path, key, map[string]any{"commitSha": "different"})
	if w.Code != 409 {
		t.Fatalf("changed inputs: %d %s", w.Code, w.Body.String())
	}
	deployments, err := a.store.ListDeployments(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, d := range deployments {
		if d.AppID == app.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("created %d logical deployments", count)
	}
	audit, err := a.store.(operationsStore).ListAuditEvents(context.Background(), core.AuditFilter{AppID: app.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	linked := 0
	for _, e := range audit {
		if e.OperationID == operationID {
			linked++
		}
	}
	if linked < 2 {
		t.Fatalf("acceptance/replay audit lost original operation: %#v", audit)
	}
	w = mutationRequest(a, "secret", "GET", result.OperationURL, "", nil)
	if w.Code != 200 {
		t.Fatal("existing deployment inspection unavailable")
	}
	w = mutationRequest(a, "secret", "GET", result.OperationURL+"/logs", "", nil)
	if w.Code != 200 {
		t.Fatal("existing deployment logs unavailable")
	}
}

func TestMutationReceiptExpiredReplayAndPreparationRecovery(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted-preparation", true: "expired-key"}[expired], func(t *testing.T) {
			a := serviceTestAPI(t)
			app := mutationApp(t, a)
			kind, caller := mutationCaller(controllerIdentity("owner"))
			key := "preparation-retry-key"
			input := struct {
				CommitSHA string                 `json:"commitSha"`
				Review    *core.DeploymentReview `json:"review,omitempty"`
			}{}
			receipt := core.MutationReceipt{ID: "mutation-" + mutationHash([]string{kind, caller, app.ProjectID, "deployment.start", key}), CallerKind: kind, CallerID: caller, ProjectID: app.ProjectID, Action: "deployment.start", KeyDigest: mutationHash(key), RequestDigest: mutationHash(input), OperationKind: "deployment", OperationID: "original-operation", ResourceID: app.ID, ClaimToken: "lost-controller"}
			now := time.Now().Add(-2 * time.Minute)
			if expired {
				now = time.Now().Add(-store.MutationRetryWindow - time.Minute)
			}
			if _, _, err := a.store.(store.MutationReceiptStore).ReserveMutationReceipt(context.Background(), receipt, now); err != nil {
				t.Fatal(err)
			}
			w := mutationRequest(a, "secret", "POST", "/api/v1/apps/"+app.ID+"/deployments", key, map[string]any{})
			if expired {
				if w.Code != 409 {
					t.Fatalf("expired key repeated work: %d %s", w.Code, w.Body.String())
				}
				if _, err := a.store.GetDeployment(context.Background(), receipt.OperationID); err == nil {
					t.Fatal("expired key created deployment")
				}
				return
			}
			result := decodeMutation(t, w)
			if w.Code != 202 || result.OperationID != receipt.OperationID {
				t.Fatalf("preparation lost original operation: %d %s", w.Code, w.Body.String())
			}
			awaitMutationDeployment(t, a, result.OperationID)
		})
	}
}

func TestMutationReceiptAutomationRotationRevocationAndCallerIsolation(t *testing.T) {
	a := serviceTestAPI(t)
	app := mutationApp(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	newAccount := func(name string) (core.ServiceAccount, core.AutomationCredential, string) {
		var account core.ServiceAccount
		w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": name}, 201)
		json.Unmarshal(w.Body.Bytes(), &account)
		var issued struct {
			Credential core.AutomationCredential `json:"credential"`
			Token      string                    `json:"token"`
		}
		w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "ci", "expiresAt": now.Add(time.Hour)}, 201)
		json.Unmarshal(w.Body.Bytes(), &issued)
		grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: app.ProjectID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionDeploymentRun}}
		if err := a.store.(store.AutomationStore).SavePrincipalGrant(ctx, grant); err != nil {
			t.Fatal(err)
		}
		return account, issued.Credential, issued.Token
	}
	account, credential, token := newAccount("pipeline")
	_, _, otherToken := newAccount("other-pipeline")
	path := "/api/v1/apps/" + app.ID + "/deployments"
	key := "pipeline-deployment-key"
	w := mutationRequest(a, token, "POST", path, key, map[string]any{})
	if w.Code != 202 {
		t.Fatalf("accept: %d %s", w.Code, w.Body.String())
	}
	receipt := decodeMutation(t, w)
	awaitMutationDeployment(t, a, receipt.OperationID)
	url := "/api/v1/mutation-receipts/" + receipt.ID
	if w = mutationRequest(a, otherToken, "GET", url, "", nil); w.Code != 404 {
		t.Fatalf("another caller read receipt: %d", w.Code)
	}
	var rotated struct {
		Credential core.AutomationCredential `json:"credential"`
		Token      string                    `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials/"+credential.ID+"/rotate", map[string]any{"name": "rotated", "expiresAt": now.Add(time.Hour)}, 201)
	json.Unmarshal(w.Body.Bytes(), &rotated)
	if w = mutationRequest(a, token, "GET", url, "", nil); w.Code != 401 {
		t.Fatal("revoked credential retrieved receipt")
	}
	w = mutationRequest(a, rotated.Token, "POST", path, key, map[string]any{})
	if w.Code != 202 || decodeMutation(t, w).OperationID != receipt.OperationID {
		t.Fatalf("rotation changed operation: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), token) || strings.Contains(w.Body.String(), key) {
		t.Fatal("receipt leaked request credential/key")
	}
	if err := a.store.(store.AutomationStore).SavePrincipalGrant(ctx, core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: app.ProjectID, Permissions: []core.Permission{}}); err != nil {
		t.Fatal(err)
	}
	if w = mutationRequest(a, rotated.Token, "POST", path, key, map[string]any{}); w.Code != 403 {
		t.Fatal("lost project access replayed operation")
	}
	if w = mutationRequest(a, rotated.Token, "GET", url, "", nil); w.Code != 403 {
		t.Fatal("lost project access retrieved receipt")
	}
}

func TestMutationReceiptInfrastructureCreateAndDelete(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	adapter, err := mock.New(mock.Options{})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(provider.Handler(adapter, ""))
	defer upstream.Close()
	p, err := a.infrastructureManager().Register(ctx, provision.Registration{Name: "Receipt provider", Endpoint: upstream.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete}})
	if err != nil {
		t.Fatal(err)
	}
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = a.store.CreateSecret(ctx, core.Secret{ID: "receipt-ssh", Name: "Receipt SSH", Type: core.SecretTypeSSHPrivateKey, Source: core.SecretSourceLocal, PublicValue: "ssh-ed25519 fixture", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	input := provision.CreateInput{ProjectID: projects[0].ID, ProviderID: p.ID, Name: "Receipt server", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "receipt-ssh", Config: map[string]any{}}
	var review core.InfrastructureReview
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/review", input, 201), &review); err != nil {
		t.Fatal(err)
	}
	acceptance := provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: input.Name}
	concurrent := func(path, key string, body provision.Acceptance) mutationReceiptResponse {
		t.Helper()
		var wg sync.WaitGroup
		results := make(chan *httptest.ResponseRecorder, 8)
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); results <- mutationRequest(a, "secret", "POST", path, key, body) }()
		}
		wg.Wait()
		close(results)
		var original mutationReceiptResponse
		for w := range results {
			if w.Code != 202 {
				t.Fatalf("concurrent acceptance: %d %s", w.Code, w.Body.String())
			}
			receipt := decodeMutation(t, w)
			if original.ID == "" {
				original = receipt
			}
			if receipt.ID != original.ID || receipt.OperationID != original.OperationID || receipt.ResourceID != review.ServerID {
				t.Fatal("retry changed the owned operation", receipt)
			}
		}
		return original
	}
	path := "/api/v1/infrastructure/servers"
	created := concurrent(path, "create-server-receipt", acceptance)
	{
		changed := acceptance
		changed.ConfirmName = "different"
		if w := mutationRequest(a, "secret", "POST", path, "create-server-receipt", changed); w.Code != 409 {
			t.Fatalf("changed acceptance: %d %s", w.Code, w.Body.String())
		}
	}
	if w := mutationRequest(a, "secret", "POST", path, "another-server-key", acceptance); w.Code != 409 {
		t.Fatalf("review accepted twice: %d %s", w.Code, w.Body.String())
	}
	data := a.store.(store.InfrastructureLifecycleStore)
	manager := a.infrastructureManager()
	manager.Now = func() time.Time { return now }
	settle := func() {
		t.Helper()
		for range 5 {
			now = now.Add(3 * time.Second)
			if _, err := manager.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	settle()
	assertReplay := func(path, key string, body provision.Acceptance, original mutationReceiptResponse) {
		t.Helper()
		w := mutationRequest(a, "secret", "POST", path, key, body)
		replayed := decodeMutation(t, w)
		if w.Code != 202 || replayed.OperationID != original.OperationID || replayed.State != "succeeded" || w.Header().Get("Idempotency-Replayed") != "true" {
			t.Fatalf("terminal replay: %d %s", w.Code, w.Body.String())
		}
		if w = mutationRequest(a, "secret", "GET", replayed.OperationURL, "", nil); w.Code != 200 {
			t.Fatalf("original history: %d %s", w.Code, w.Body.String())
		}
		w = mutationRequest(a, "secret", "GET", "/api/v1/mutation-receipts/"+original.ID, "", nil)
		if w.Code != 200 || decodeMutation(t, w).State != "succeeded" {
			t.Fatalf("terminal receipt: %d %s", w.Code, w.Body.String())
		}
	}
	assertReplay(path, "create-server-receipt", acceptance, created)
	server, err := data.GetManagedServer(ctx, review.ServerID)
	if err != nil || server.AllocationState != "allocated" {
		t.Fatalf("allocation: %#v %v", server, err)
	}
	if _, err = adapter.Server(ctx, server.ResourceID); err != nil {
		t.Fatal("accepted provider server missing", err)
	}
	var deletion provision.DeletionReview
	if err = json.Unmarshal(serviceRequestTest(t, a, "POST", path+"/"+server.ID+"/delete-review", nil, 200), &deletion); err != nil {
		t.Fatal(err)
	}
	deleteInput := provision.Acceptance{Digest: deletion.Digest, ConfirmName: server.Name}
	deletePath := path + "/" + server.ID + "/delete"
	deleted := concurrent(deletePath, "delete-server-receipt", deleteInput)
	settle()
	assertReplay(deletePath, "delete-server-receipt", deleteInput, deleted)
	server, err = data.GetManagedServer(ctx, server.ID)
	if err != nil || server.AllocationState != "deleted" {
		t.Fatalf("deletion: %#v %v", server, err)
	}
	operations, err := data.ListInfrastructureOperations(ctx, server.ID)
	if err != nil || len(operations) != 2 {
		t.Fatalf("accepted %d operations: %v", len(operations), err)
	}
	for _, receipt := range []mutationReceiptResponse{created, deleted} {
		saved, err := a.store.(store.MutationReceiptStore).GetMutationReceipt(ctx, receipt.ID)
		if err != nil || saved.State != "succeeded" {
			t.Fatalf("terminal evidence was not persisted: %#v %v", saved, err)
		}
	}
	events, err := a.store.(operationsStore).ListAuditEvents(ctx, core.AuditFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{created.OperationID, deleted.OperationID} {
		linked := 0
		for _, event := range events {
			if event.OperationID == id {
				linked++
			}
		}
		if linked < 2 {
			t.Fatalf("missing acceptance/replay audit for %s", id)
		}
	}
}

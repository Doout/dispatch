package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
)

func TestSnapshotAPIReceiptsScopedPermissionsAndQuota(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	adapter, _ := mock.New(mock.Options{Polls: 1})
	endpoint := httptest.NewServer(provider.Handler(adapter, ""))
	defer endpoint.Close()
	caps := []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete, provider.CapabilitySnapshotCreate, provider.CapabilitySnapshotInspect, provider.CapabilitySnapshotDelete, provider.CapabilityRestore}
	var registered core.InfrastructureProvider
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", provision.Registration{Name: "Snapshot fixture", Endpoint: endpoint.URL, Enabled: true, Capabilities: caps}, 201)
	json.Unmarshal(raw, &registered)
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0]
	now := time.Now().UTC()
	a.store.CreateSecret(ctx, core.Secret{ID: "snapshot-ssh", Name: "SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 public-fixture", CreatedAt: now, UpdatedAt: now})
	policy := core.InfrastructureQuotaPolicy{MaxServers: 2, Providers: []core.InfrastructureProviderRule{{ProviderID: registered.ID, AnyRegion: true, AnySize: true}}}
	quotaPath := "/api/v1/projects/" + project.ID + "/infrastructure/quota"
	serviceRequestTest(t, a, "PUT", quotaPath, policy, 200)
	input := provision.CreateInput{ProjectID: project.ID, ProviderID: registered.ID, Name: "source", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "snapshot-ssh", Config: map[string]any{}}
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/review", input, 201)
	var create core.InfrastructureReview
	json.Unmarshal(raw, &create)
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", provision.Acceptance{ReviewID: create.ID, Digest: create.Digest, ConfirmName: create.Name, RequestKey: "create-source"}, 202)
	var accepted provision.Accepted
	json.Unmarshal(raw, &accepted)
	m := a.infrastructureManager()
	m.Authorize = nil
	m.Now = func() time.Time { return now }
	for range 4 {
		now = now.Add(3 * time.Second)
		if _, err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var account core.ServiceAccount
	w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "snapshot-agent"}, 201)
	json.Unmarshal(w.Body.Bytes(), &account)
	var credential struct {
		Token string `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "token", "expiresAt": time.Now().Add(time.Hour)}, 201)
	json.Unmarshal(w.Body.Bytes(), &credential)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: project.ID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionInfrastructureInspect, core.PermissionSnapshotCreate}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	reviewPath := "/api/v1/infrastructure/servers/" + accepted.Server.ID + "/snapshot-review"
	capture := provision.SnapshotInput{Name: "retained", DiskSet: "all", Consistency: provider.ConsistencyCrash, Encryption: provider.SnapshotEncryption{Mode: "provider-managed"}, RetainUntil: time.Now().Add(time.Hour)}
	automationRequest(t, a, credential.Token, "POST", reviewPath, capture, 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": "provider", "resourceId": registered.ID}, 200)
	w = automationRequest(t, a, credential.Token, "POST", reviewPath, capture, 201)
	var review core.InfrastructureSnapshotReview
	json.Unmarshal(w.Body.Bytes(), &review)
	accept := provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name}
	path := "/api/v1/infrastructure/snapshots/accept"
	w = mutationRequest(a, credential.Token, "POST", path, "quota-denied", accept)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if items, _ := adapter.Snapshots(ctx, accepted.Server.ResourceID); len(items) != 0 {
		t.Fatal("denied admission reached provider")
	}
	policy.Revision = 1
	policy.MaxSnapshots = 1
	serviceRequestTest(t, a, "PUT", quotaPath, policy, 200)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- mutationRequest(a, credential.Token, "POST", path, "accepted-capture", accept)
		}()
	}
	wg.Wait()
	close(responses)
	var operationID, receiptID string
	for response := range responses {
		if response.Code != 202 {
			t.Fatal(response.Code, response.Body.String())
		}
		receipt := decodeMutation(t, response)
		if operationID != "" && operationID != receipt.OperationID {
			t.Fatal("duplicate capture receipt operation")
		}
		operationID = receipt.OperationID
		receiptID = receipt.ID
	}
	for range 5 {
		now = now.Add(3 * time.Second)
		if _, err := m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	op, err := a.store.(store.InfrastructureLifecycleStore).GetInfrastructureOperation(ctx, operationID)
	if err != nil || op.State != "succeeded" {
		t.Fatal(op, err)
	}
	w = mutationRequest(a, credential.Token, "POST", path, "accepted-capture", accept)
	if w.Code != 202 || decodeMutation(t, w).State != "succeeded" {
		t.Fatal(w.Code, w.Body.String())
	}
	automationRequest(t, a, credential.Token, "GET", "/api/v1/mutation-receipts/"+receiptID, nil, 200)
	w = automationRequest(t, a, credential.Token, "GET", "/api/v1/projects/"+project.ID+"/infrastructure/snapshots", nil, 200)
	var snapshots []core.InfrastructureSnapshot
	json.Unmarshal(w.Body.Bytes(), &snapshots)
	if len(snapshots) != 1 || snapshots[0].State != "ready" {
		t.Fatal(w.Body.String())
	}
	automationRequest(t, a, credential.Token, "POST", "/api/v1/infrastructure/snapshots/"+snapshots[0].ID+"/delete-review", nil, 403)
	// Current grants are required even for a receipt replay.
	grant.Permissions = []core.Permission{core.PermissionProjectView, core.PermissionInfrastructureInspect}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	w = mutationRequest(a, credential.Token, "POST", path, "accepted-capture", accept)
	if w.Code != 403 {
		t.Fatal("revoked capture permission replayed", w.Code)
	}
}

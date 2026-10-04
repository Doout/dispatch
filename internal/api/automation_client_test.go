package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/automationclient"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
)

func TestAutomationClientSimulationAndMockProvider(t *testing.T) {
	a := serviceTestAPI(t)
	app := mutationApp(t, a)
	ctx := context.Background()
	var account core.ServiceAccount
	w := automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts", map[string]any{"name": "cli-agent"}, 201)
	json.Unmarshal(w.Body.Bytes(), &account)
	var issued struct {
		Token string `json:"token"`
	}
	w = automationRequest(t, a, "secret", "POST", "/api/v1/automation-accounts/"+account.ID+"/credentials", map[string]any{"name": "cli", "expiresAt": time.Now().Add(time.Hour)}, 201)
	json.Unmarshal(w.Body.Bytes(), &issued)
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: app.ProjectID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionDeploymentRun, core.PermissionInfrastructureInspect, core.PermissionInfrastructureCreate, core.PermissionSnapshotCreate}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/grants", grant, 200)
	server := httptest.NewServer(a)
	defer server.Close()
	client, e := automationclient.New(server.URL, issued.Token, 30*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	call := func(name string, args automationclient.Arguments) automationclient.Result {
		t.Helper()
		r := client.Call(ctx, name, args)
		if !r.OK {
			b, _ := json.Marshal(r)
			t.Fatal(name, string(b))
		}
		return r
	}
	projects := call("projects_list", automationclient.Arguments{})
	var visible []core.Project
	json.Unmarshal(projects.Data, &visible)
	if len(visible) != 1 || visible[0].ID != app.ProjectID {
		t.Fatal("client crossed project scope")
	}
	preview := call("deployment_preview", automationclient.Arguments{AppID: app.ID, Revision: "inline"})
	var plan struct {
		Review core.DeploymentReview `json:"review"`
	}
	json.Unmarshal(preview.Data, &plan)
	body, _ := json.Marshal(automationclient.DeploymentStart{CommitSHA: "inline", Review: &plan.Review})
	args := automationclient.Arguments{AppID: app.ID, Key: "cli-deploy-key", Input: body}
	first := call("deployment_start", args)
	var receipt core.MutationReceipt
	json.Unmarshal(first.Data, &receipt)
	if receipt.OperationID == "" || receipt.ID == "" {
		t.Fatal("missing operation identity")
	}
	replay := call("deployment_start", args)
	if !replay.Replayed {
		t.Fatal("client duplicated accepted operation")
	}
	call("receipt_wait", automationclient.Arguments{ReceiptID: receipt.ID, TimeoutSeconds: 10})
	logs := call("deployment_logs", automationclient.Arguments{DeploymentID: receipt.OperationID, Limit: 2})
	var entries []core.DeploymentLog
	json.Unmarshal(logs.Data, &entries)
	if len(entries) > 2 {
		t.Fatal("unbounded logs")
	}
	call("deployment_diagnose", automationclient.Arguments{DeploymentID: receipt.OperationID})
	if r := client.Call(ctx, "server_delete_review", automationclient.Arguments{ServerID: "not-owned"}); r.OK {
		t.Fatal("client bypassed authorization")
	}
	adapter, _ := mock.New(mock.Options{})
	endpoint := httptest.NewServer(provider.Handler(adapter, ""))
	defer endpoint.Close()
	var p core.InfrastructureProvider
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", provision.Registration{Name: "CLI mock", Endpoint: endpoint.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete, provider.CapabilitySnapshotCreate, provider.CapabilitySnapshotInspect, provider.CapabilitySnapshotDelete}}, 201)
	json.Unmarshal(raw, &p)
	now := time.Now().UTC()
	if e = a.store.CreateSecret(ctx, core.Secret{ID: "cli-ssh", Name: "Public SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 fixture", CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	for kind, id := range map[string]string{"provider": p.ID, "ssh_key": "cli-ssh"} {
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+app.ProjectID, map[string]any{"kind": kind, "resourceId": id}, 200)
	}
	catalog := call("providers_list", automationclient.Arguments{ProjectID: app.ProjectID})
	if !strings.Contains(string(catalog.Data), p.ID) {
		t.Fatal("assigned provider missing")
	}
	options := call("provider_options", automationclient.Arguments{ProjectID: app.ProjectID, ProviderID: p.ID, Input: json.RawMessage(`{"kind":"sizes","config":{}}`)})
	if !strings.Contains(string(options.Data), "mock-small") {
		t.Fatal("provider choices missing")
	}
	policy := core.InfrastructureQuotaPolicy{MaxServers: 1, MaxSnapshots: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, Regions: []string{"mock-region"}, Sizes: []string{"mock-small"}}}}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+app.ProjectID+"/infrastructure/quota", policy, 200)
	create := automationclient.ServerCreateReview{ProjectID: app.ProjectID, ProviderID: p.ID, Name: "CLI server", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "cli-ssh", Config: map[string]any{}}
	raw, _ = json.Marshal(create)
	review := call("server_review", automationclient.Arguments{Input: raw})
	var allocation core.InfrastructureReview
	json.Unmarshal(review.Data, &allocation)
	raw, _ = json.Marshal(automationclient.InfrastructureAcceptance{ReviewID: allocation.ID, Digest: allocation.Digest, ConfirmName: create.Name})
	accept := automationclient.Arguments{Key: "cli-server-create", Input: raw}
	first = call("server_create", accept)
	json.Unmarshal(first.Data, &receipt)
	replay = call("server_create", accept)
	if !replay.Replayed {
		t.Fatal("provider request duplicated")
	}
	manager := a.infrastructureManager()
	manager.Now = func() time.Time { return now }
	for range 5 {
		now = now.Add(3 * time.Second)
		if _, e = manager.Reconcile(ctx); e != nil {
			t.Fatal(e)
		}
	}
	call("receipt_wait", automationclient.Arguments{ReceiptID: receipt.ID, TimeoutSeconds: 5})
	inventory := call("servers_list", automationclient.Arguments{ProjectID: app.ProjectID})
	if !strings.Contains(string(inventory.Data), allocation.ServerID) {
		t.Fatal("accepted server missing")
	}
	readiness := call("server_get", automationclient.Arguments{ServerID: allocation.ServerID})
	var status provision.ServerReadiness
	if json.Unmarshal(readiness.Data, &status) != nil || status.ID != allocation.ServerID || status.WaitState != "waiting" || status.AllocationState != "allocated" || status.Deployable || readiness.Continuation.OperationID == "" {
		t.Fatal("allocation success became deployment readiness", readiness)
	}
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/infrastructure/servers/"+allocation.ServerID, nil, 200)
	if w.Header().Get("Cache-Control") != "no-store" || strings.Contains(w.Body.String(), "encryptedInput") || strings.Contains(w.Body.String(), "requestDigest") {
		t.Fatal("readiness response cached or leaked private operation inputs")
	}
	waitCtx, stopWaiting := context.WithTimeout(ctx, 5*time.Second)
	waited := client.Call(waitCtx, "server_wait", automationclient.Arguments{ServerID: allocation.ServerID, TimeoutSeconds: 2})
	stopWaiting()
	if waited.OK || waited.ExitCode() != 4 || waited.Continuation == nil || waited.Continuation.ID != allocation.ServerID || waited.Continuation.OperationID != readiness.Continuation.OperationID {
		t.Fatalf("wait lost the original allocated server: %+v error=%+v continuation=%+v", waited, waited.Error, waited.Continuation)
	}
	// Scoped inspection returns only accessible bootstrap records.
	w = automationRequest(t, a, issued.Token, "GET", "/api/v1/infrastructure/bootstrap", nil, 200)
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("bootstrap inspection returned unrelated records", w.Body.String())
	}
	// Server inspection still requires its current provider assignment.
	automationRequest(t, a, "secret", "DELETE", "/api/v1/infrastructure/assignments/"+app.ProjectID+"/provider/"+p.ID, nil, 204)
	if denied := client.Call(ctx, "server_get", automationclient.Arguments{ServerID: allocation.ServerID}); denied.Status != 403 {
		t.Fatal("readiness ignored revoked provider assignment", denied)
	}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+app.ProjectID, map[string]any{"kind": "provider", "resourceId": p.ID}, 200)
	// Read and create permissions do not authorize deleting infrastructure.
	if r := client.Call(ctx, "server_delete_review", automationclient.Arguments{ServerID: allocation.ServerID}); r.Status != 403 || r.ExitCode() != 3 {
		t.Fatal("client gained deletion permission", r)
	}
	snapshotReview := call("snapshot_review", automationclient.Arguments{ServerID: allocation.ServerID, Input: json.RawMessage(`{"name":"cli-snapshot","diskSet":"all","consistency":"crash-consistent","encryption":{"mode":"provider-managed"}}`)})
	var capture core.InfrastructureSnapshotReview
	json.Unmarshal(snapshotReview.Data, &capture)
	raw, _ = json.Marshal(automationclient.InfrastructureAcceptance{ReviewID: capture.ID, Digest: capture.Digest, ConfirmName: capture.Name})
	captureArgs := automationclient.Arguments{Key: "cli-snapshot-capture", Input: raw}
	first = call("snapshot_accept", captureArgs)
	json.Unmarshal(first.Data, &receipt)
	if replay := call("snapshot_accept", captureArgs); !replay.Replayed {
		t.Fatal("snapshot capture duplicated")
	}
	for range 5 {
		now = now.Add(3 * time.Second)
		if _, e = manager.Reconcile(ctx); e != nil {
			t.Fatal(e)
		}
	}
	call("receipt_wait", automationclient.Arguments{ReceiptID: receipt.ID, TimeoutSeconds: 5})
	snapshots := call("snapshots_list", automationclient.Arguments{ProjectID: app.ProjectID})
	if !strings.Contains(string(snapshots.Data), capture.SnapshotID) {
		t.Fatal("accepted snapshot missing")
	}
	snapshot := call("snapshot_get", automationclient.Arguments{SnapshotID: capture.SnapshotID})
	var owned core.InfrastructureSnapshot
	json.Unmarshal(snapshot.Data, &owned)
	if owned.State != "ready" {
		t.Fatal("snapshot is not ready")
	}
	if r := client.Call(ctx, "snapshot_delete_review", automationclient.Arguments{SnapshotID: capture.SnapshotID}); r.Status != 403 || r.ExitCode() != 3 {
		t.Fatal("client gained snapshot deletion permission", r)
	}
	foreign := core.Project{ID: "foreign-managed-project", Name: "Foreign managed project", CreatedAt: now}
	if err := a.store.CreateProject(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	automationRequest(t, a, "secret", "PUT", "/api/v1/projects/"+foreign.ID+"/infrastructure/quota", policy, 200)
	foreignInput := create
	foreignInput.ProjectID, foreignInput.Name = foreign.ID, "foreign-machine"
	var foreignReview core.InfrastructureReview
	if json.Unmarshal(serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/review", foreignInput, 201), &foreignReview) != nil {
		t.Fatal("foreign scope fixture did not create a review")
	}
	serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", provision.Acceptance{ReviewID: foreignReview.ID, Digest: foreignReview.Digest, ConfirmName: foreignReview.Name, RequestKey: "foreign-fixture-once"}, 202)
	for _, name := range []string{"server_get", "server_wait"} {
		if denied := client.Call(ctx, name, automationclient.Arguments{ServerID: foreignReview.ServerID}); denied.Status != 403 {
			t.Fatal("managed readiness crossed the project scope", name, denied)
		}
	}
	automationRequest(t, a, "secret", "DELETE", "/api/v1/infrastructure/grants/service_account/"+account.ID+"/"+app.ProjectID, nil, 204)
	for _, name := range []string{"server_get", "server_wait"} {
		if denied := client.Call(ctx, name, automationclient.Arguments{ServerID: allocation.ServerID}); denied.Status != 403 || denied.Continuation == nil || denied.Continuation.ID != allocation.ServerID {
			t.Fatal("readiness ignored revoked project permission", name, denied)
		}
	}
	if r := client.Call(ctx, "server_create", accept); r.Status != 403 {
		t.Fatal("replay ignored revoked permission", r)
	}
	if r := client.Call(ctx, "snapshot_accept", captureArgs); r.Status != 403 {
		t.Fatal("snapshot replay ignored revoked permission", r)
	}
}

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
	grant := core.PrincipalGrant{PrincipalType: core.PrincipalServiceAccount, PrincipalID: account.ID, ProjectID: app.ProjectID, Permissions: []core.Permission{core.PermissionProjectView, core.PermissionDeploymentRun, core.PermissionInfrastructureInspect, core.PermissionInfrastructureCreate}}
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
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/providers", provision.Registration{Name: "CLI mock", Endpoint: endpoint.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete}}, 201)
	json.Unmarshal(raw, &p)
	now := time.Now().UTC()
	if e = a.store.CreateSecret(ctx, core.Secret{ID: "cli-ssh", Name: "Public SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 fixture", CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	for kind, id := range map[string]string{"provider": p.ID, "ssh_key": "cli-ssh"} {
		automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+app.ProjectID, map[string]any{"kind": kind, "resourceId": id}, 200)
	}
	policy := core.InfrastructureQuotaPolicy{MaxServers: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, Regions: []string{"mock-region"}, Sizes: []string{"mock-small"}}}}
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
	// Read and create permissions do not authorize deleting infrastructure.
	if r := client.Call(ctx, "server_delete_review", automationclient.Arguments{ServerID: allocation.ServerID}); r.Status != 403 || r.ExitCode() != 3 {
		t.Fatal("client gained deletion permission", r)
	}
	automationRequest(t, a, "secret", "DELETE", "/api/v1/infrastructure/grants/service_account/"+account.ID+"/"+app.ProjectID, nil, 204)
	if r := client.Call(ctx, "server_create", accept); r.Status != 403 {
		t.Fatal("replay ignored revoked permission", r)
	}
}

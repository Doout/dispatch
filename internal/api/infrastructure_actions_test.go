package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
)

func machineActionRequestTest(t *testing.T, a *API, token, method, path, key string, input any, want int) *httptest.ResponseRecorder {
	t.Helper()
	raw, _ := json.Marshal(input)
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return w
}
func TestManagedPowerAPIReceiptScopeAndRecoveryRoutes(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, e := a.store.ListProjects(ctx)
	if e != nil || len(projects) == 0 {
		t.Fatal(e)
	}
	project := projects[0]
	adapter, _ := mock.New(mock.Options{Polls: 1})
	upstream := httptest.NewServer(provider.Handler(adapter, ""))
	defer upstream.Close()
	p, e := a.infrastructureManager().Register(ctx, provision.Registration{Name: "Power API fixture", Endpoint: upstream.URL, Enabled: true, Capabilities: []string{provider.CapabilityCreate, provider.CapabilityInspect, provider.CapabilityDelete, provider.CapabilityPowerStart, provider.CapabilityPowerStop, provider.CapabilityPowerReboot}})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	if e = a.store.CreateSecret(ctx, core.Secret{ID: "power-public-key", Name: "Public SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 fixture", CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/projects/"+project.ID+"/infrastructure/quota", core.InfrastructureQuotaPolicy{MaxServers: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, AnyRegion: true, AnySize: true}}}, 200)
	in := provision.CreateInput{ProjectID: project.ID, ProviderID: p.ID, Name: "Power fixture", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "power-public-key", Config: map[string]any{}}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/review", in, 201)
	var review core.InfrastructureReview
	if json.Unmarshal(raw, &review) != nil {
		t.Fatal("review")
	}
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "power-api-create"}, 202)
	var created provision.Accepted
	if json.Unmarshal(raw, &created) != nil {
		t.Fatal("acceptance")
	}
	m := a.infrastructureManager()
	m.Now = func() time.Time { return now }
	for range 4 {
		now = now.Add(3 * time.Second)
		if _, e = m.Reconcile(ctx); e != nil {
			t.Fatal(e)
		}
	}
	data := a.store.(store.InfrastructureLifecycleStore)
	s, e := data.GetManagedServer(ctx, review.ServerID)
	if e != nil {
		t.Fatal(e)
	}
	path := "/api/v1/infrastructure/servers/" + s.ID + "/power"
	input := provision.PowerInput{Action: "stop", Revision: s.Revision}
	token, grant := scopedServiceIdentity(t, a, project.ID, core.PermissionProjectView, core.PermissionInfrastructureInspect)
	machineActionRequestTest(t, a, token, "POST", path, "forbidden-power", input, 403)
	grant.Permissions = append(grant.Permissions, core.PermissionInfrastructureModify)
	grant.UpdatedAt = time.Now()
	if e = a.store.(store.AutomationStore).SavePrincipalGrant(ctx, grant); e != nil {
		t.Fatal(e)
	}
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": "provider", "resourceId": p.ID}, 200)
	machineActionRequestTest(t, a, token, "POST", path, "", input, 422)
	response := machineActionRequestTest(t, a, token, "POST", path, "power-api-once", input, 202)
	var receipt core.MutationReceipt
	if json.Unmarshal(response.Body.Bytes(), &receipt) != nil || receipt.OperationKind != "infrastructure_operation" || receipt.ResourceID != s.ID {
		t.Fatal("power receipt lost identity", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "encryptedRequest") || strings.Contains(response.Body.String(), "expectedIdentity") {
		t.Fatal("private action payload returned")
	}
	replay := machineActionRequestTest(t, a, token, "POST", path, "power-api-once", input, 202)
	var again core.MutationReceipt
	if json.Unmarshal(replay.Body.Bytes(), &again) != nil || again.OperationID != receipt.OperationID {
		t.Fatal("receipt replay scheduled replacement")
	}
	changed := input
	changed.Revision++
	machineActionRequestTest(t, a, token, "POST", path, "power-api-once", changed, 409)
	for range 4 {
		now = now.Add(3 * time.Second)
		if _, e = m.Reconcile(ctx); e != nil {
			t.Fatal(e)
		}
	}
	op, e := data.GetInfrastructureOperation(ctx, receipt.OperationID)
	if e != nil || op.State != "succeeded" {
		t.Fatal("power action did not finish", op, e)
	}
	status := machineActionRequestTest(t, a, token, "GET", "/api/v1/infrastructure/servers/"+s.ID, "", nil, 200)
	if !strings.Contains(status.Body.String(), `"powerState":"stopped"`) || strings.Contains(status.Body.String(), `"deployable":true`) {
		t.Fatal("stopped machine became deployable", status.Body.String())
	}
	grant.Permissions = []core.Permission{core.PermissionProjectView, core.PermissionInfrastructureInspect}
	grant.UpdatedAt = time.Now()
	if e = a.store.(store.AutomationStore).SavePrincipalGrant(ctx, grant); e != nil {
		t.Fatal(e)
	}
	machineActionRequestTest(t, a, token, "POST", path, "power-api-once", input, 403)
	machineActionRequestTest(t, a, "secret", "GET", "/api/v1/infrastructure/servers/"+s.ID+"/clone", "", nil, 422)
}

func TestClonePromotionAPIGrantsReceiptAndOriginalResolution(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	a.auth.PublicURL = "https://dispatch.example.com"
	root := t.TempDir()
	t.Setenv("DISPATCH_EDGE_BINARY_ROOT", root)
	if e := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("pinned promotion fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	adapter, e := mock.New(mock.Options{Polls: 1, FailPromotion: true})
	if e != nil {
		t.Fatal(e)
	}
	upstream := httptest.NewServer(provider.Handler(adapter, ""))
	defer upstream.Close()
	manifest, _ := adapter.Manifest(ctx)
	p, e := a.infrastructureManager().Register(ctx, provision.Registration{Name: "Clone API fixture", Endpoint: upstream.URL, Enabled: true, Capabilities: manifest.Capabilities})
	if e != nil {
		t.Fatal(e)
	}
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0]
	now := time.Now()
	if e = a.store.CreateSecret(ctx, core.Secret{ID: "clone-public-key", Name: "Public SSH", Type: core.SecretTypeSSHPrivateKey, PublicValue: "ssh-ed25519 fixture", CreatedAt: now, UpdatedAt: now}); e != nil {
		t.Fatal(e)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/projects/"+project.ID+"/infrastructure/quota", core.InfrastructureQuotaPolicy{MaxServers: 2, MaxSnapshots: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, AnyRegion: true, AnySize: true}}}, 200)
	m := a.infrastructureManager()
	finish := func(id string) core.InfrastructureOperation {
		t.Helper()
		for range 8 {
			if _, e := m.Reconcile(ctx); e != nil {
				t.Fatal(e)
			}
			o, e := a.store.(store.InfrastructureLifecycleStore).GetInfrastructureOperation(ctx, id)
			if e != nil {
				t.Fatal(e)
			}
			if o.State == "succeeded" || o.State == "unknown" || o.State == "failed" {
				return o
			}
		}
		t.Fatal("operation did not finish")
		return core.InfrastructureOperation{}
	}
	create := func(in provision.CreateInput, key string) core.ManagedServer {
		t.Helper()
		raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/review", in, 201)
		var review core.InfrastructureReview
		if json.Unmarshal(raw, &review) != nil {
			t.Fatal("create review")
		}
		raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers", provision.Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: key}, 202)
		var accepted provision.Accepted
		if json.Unmarshal(raw, &accepted) != nil {
			t.Fatal("accepted server")
		}
		if o := finish(accepted.Operation.ID); o.State != "succeeded" {
			t.Fatal(o)
		}
		s, e := a.store.(store.InfrastructureLifecycleStore).GetManagedServer(ctx, accepted.Server.ID)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	in := provision.CreateInput{ProjectID: project.ID, ProviderID: p.ID, Name: "Snapshot source", Region: "mock-region", Size: "mock-small", Image: "mock-linux", Network: "mock-private", SSHKeySecretID: "clone-public-key", Config: map[string]any{}}
	source := create(in, "clone-api-source")
	raw := serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/servers/"+source.ID+"/snapshot-review", provision.SnapshotInput{Name: "Recovery snapshot", DiskSet: "all", Consistency: provider.ConsistencyCrash, Encryption: provider.SnapshotEncryption{Mode: "provider-managed"}}, 201)
	var snapshotReview core.InfrastructureSnapshotReview
	if json.Unmarshal(raw, &snapshotReview) != nil {
		t.Fatal("snapshot review")
	}
	raw = serviceRequestTest(t, a, "POST", "/api/v1/infrastructure/snapshots/accept", provision.Acceptance{ReviewID: snapshotReview.ID, Digest: snapshotReview.Digest, ConfirmName: snapshotReview.Name, RequestKey: "clone-api-capture"}, 202)
	var captured provision.AcceptedSnapshot
	if json.Unmarshal(raw, &captured) != nil {
		t.Fatal("accepted capture")
	}
	if o := finish(captured.Operation.ID); o.State != "succeeded" {
		t.Fatal(o)
	}
	in.Name = "Snapshot clone"
	in.Network = "mock-isolated"
	in.SourceSnapshotID = captured.Snapshot.ID
	in.Bootstrap = &core.TargetBootstrapPlan{Method: "cloud_init", Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true}
	clone := create(in, "clone-api-restore")
	boot, e := a.bootstrapManager().Store.GetTargetBootstrap(ctx, clone.BootstrapID)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := a.eventConfig.Vault.Decrypt("target-bootstrap:"+boot.ID, boot.EncryptedInput)
	if e != nil {
		t.Fatal(e)
	}
	var saved struct {
		ClaimToken string `json:"claimToken"`
	}
	if json.Unmarshal(plain, &saved) != nil {
		t.Fatal("claim input")
	}
	claim, e := a.bootstrapManager().Claim(ctx, boot.ID, saved.ClaimToken)
	if e != nil {
		t.Fatal(e)
	}
	node, e := a.store.GetPrivateNetwork(ctx, clone.NodeID)
	if e != nil {
		t.Fatal(e)
	}
	node.EnrollmentToken = claim.Token
	session := enrollNodeTest(t, a, node)
	heartbeat := func() {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/edge/nodes/"+node.ID+"/runtime/jobs/next", nil)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		r.Header.Set("X-Dispatch-Runtime-Version", remoteruntime.APIVersion)
		r.Header.Set("X-Dispatch-Runtime-Capabilities", "deploy,inspect")
		r.Header.Set("X-Dispatch-Agent-Artifact", boot.Plan.ArtifactSHA256)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal("authenticated heartbeat", w.Code, w.Body.String())
		}
	}
	heartbeat()
	raw = serviceRequestTest(t, a, "GET", "/api/v1/infrastructure/servers/"+clone.ID+"/clone", nil, 200)
	var inspection provision.CloneInspection
	if json.Unmarshal(raw, &inspection) != nil || !inspection.Verified || inspection.Server.Deployable {
		t.Fatal("isolated clone inspection")
	}
	clone = inspection.Server.ManagedServer
	token, grant := scopedServiceIdentity(t, a, project.ID, core.PermissionProjectView, core.PermissionInfrastructureInspect)
	setPermissions := func(permissions ...core.Permission) {
		t.Helper()
		grant.Permissions = append([]core.Permission{core.PermissionProjectView, core.PermissionInfrastructureInspect}, permissions...)
		grant.UpdatedAt = time.Now()
		if e := a.store.(store.AutomationStore).SavePrincipalGrant(ctx, grant); e != nil {
			t.Fatal(e)
		}
	}
	path := "/api/v1/infrastructure/servers/" + clone.ID + "/promote"
	input := provision.PromotionInput{Network: "mock-private", Revision: clone.Revision, ConfirmName: clone.Name}
	setPermissions(core.PermissionInfrastructureModify)
	machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", input, 403)
	setPermissions(core.PermissionSnapshotRestore)
	machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", input, 403)
	setPermissions(core.PermissionInfrastructureModify, core.PermissionSnapshotRestore)
	machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", input, 403)
	automationRequest(t, a, "secret", "PUT", "/api/v1/infrastructure/assignments/"+project.ID, map[string]any{"kind": "provider", "resourceId": p.ID}, 200)
	response := machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", input, 202)
	var receipt core.MutationReceipt
	if json.Unmarshal(response.Body.Bytes(), &receipt) != nil || receipt.ResourceID != clone.ID || receipt.OperationKind != "infrastructure_operation" {
		t.Fatal("promotion receipt identity")
	}
	replay := machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", input, 202)
	var again core.MutationReceipt
	if json.Unmarshal(replay.Body.Bytes(), &again) != nil || again.OperationID != receipt.OperationID {
		t.Fatal("promotion replay replaced operation")
	}
	changed := input
	changed.Network = "different-network"
	machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", changed, 409)
	if o := finish(receipt.OperationID); o.State != "unknown" {
		t.Fatal("configured promotion failure not retained", o)
	}
	setPermissions(core.PermissionInfrastructureModify)
	machineActionRequestTest(t, a, token, "POST", path, "clone-promote-once", input, 403)
	resolve := "/api/v1/infrastructure/operations/" + receipt.OperationID + "/resolve"
	machineActionRequestTest(t, a, token, "POST", resolve, "", nil, 403)
	setPermissions(core.PermissionInfrastructureModify, core.PermissionSnapshotRestore)
	machineActionRequestTest(t, a, token, "POST", resolve, "", nil, 204)
	original, e := a.store.(store.InfrastructureLifecycleStore).GetInfrastructureOperation(ctx, receipt.OperationID)
	if e != nil || original.State != "failed" || original.ID != receipt.OperationID {
		t.Fatal("resolve did not inspect original failed action", original, e)
	}
	if _, e = a.store.GetServer(ctx, clone.ID); !errors.Is(e, store.ErrNotFound) {
		t.Fatal("failed promotion published workload target", e)
	}
}

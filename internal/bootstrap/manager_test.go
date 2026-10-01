package bootstrap

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
	"golang.org/x/crypto/ssh"
)

type fixture struct {
	m       *Manager
	data    *store.SQLStore
	binding Binding
	now     time.Time
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	data, err := store.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "key")
	if err = os.WriteFile(keyPath, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("pinned fixture agent artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fixture{data: data, now: time.Now().UTC(), binding: Binding{ServerID: "target", NodeID: "node-target", ReviewID: "review", ProviderID: "provider", ProjectID: "project"}}
	if err = data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	if err = data.CreateInfrastructureProvider(ctx, core.InfrastructureProvider{ID: "provider", Name: "Provider", Enabled: true, State: "ready", Revision: 1, ManifestDigest: "manifest", Manifest: json.RawMessage(`{}`), CreatedAt: f.now, UpdatedAt: f.now}); err != nil {
		t.Fatal(err)
	}
	f.m = &Manager{Store: data, Vault: vault, ControllerURL: "https://dispatch.example.com", ArtifactRoot: root, Now: func() time.Time { return f.now }, ResolveTarget: func(context.Context, string) (Binding, error) { return f.binding, nil }}
	return f
}
func (f *fixture) prepare(t *testing.T) (core.TargetBootstrap, string) {
	t.Helper()
	item, rendered, err := f.m.Prepare(context.Background(), f.binding, core.TargetBootstrapPlan{Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true, Method: "cloud_init"}, SSHCredentials{}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	review := core.InfrastructureReview{ID: item.ReviewID, ServerID: item.ServerID, ProjectID: item.ProjectID, ProviderID: item.ProviderID, ProviderRevision: 1, ManifestDigest: "manifest", Name: "Target", Input: json.RawMessage(`{}`), EncryptedRequest: "encrypted", Digest: "review", State: "open", ExpiresAt: f.now.Add(time.Minute), CreatedAt: f.now, BootstrapID: item.ID}
	if err = f.data.CreateInfrastructureReview(context.Background(), review); err != nil {
		t.Fatal(err)
	}
	return item, rendered
}
func (f *fixture) accept(ctx context.Context, item core.TargetBootstrap) (core.TargetBootstrap, error) {
	_, _, err := f.data.AcceptInfrastructureReview(ctx, item.ReviewID, "review", "operation", "owner", f.now, nil)
	if err != nil {
		return item, err
	}
	return f.data.GetTargetBootstrap(ctx, item.ID)
}
func (f *fixture) allocate(t *testing.T) {
	t.Helper()
	server, err := f.data.GetManagedServer(context.Background(), f.binding.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.ResourceID = f.binding.ResourceID
	server.AllocationState = "allocated"
	server.Revision++
	if err = f.data.UpdateManagedServer(context.Background(), server, server.Revision-1); err != nil {
		t.Fatal(err)
	}
}
func TestBootstrapClaimWaitsForApprovalAndAllocationAndReplaysLostResponse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	item, rendered := f.prepare(t)
	input, err := f.m.inputs(item)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), input.ClaimToken) || strings.Contains(string(raw), item.EncryptedInput) {
		t.Fatal("bootstrap credential exposed in review")
	}
	if !strings.HasPrefix(rendered, "#cloud-config") || !strings.Contains(rendered, "encoding: b64") {
		t.Fatal("bootstrap is not bounded cloud-init")
	}
	if _, err = f.m.Claim(ctx, item.ID, input.ClaimToken); !errors.Is(err, ErrCredential) {
		t.Fatal("unapproved claim released enrollment", err)
	}
	if _, err = f.m.Accept(ctx, item.ID, "changed"); !errors.Is(err, ErrConflict) {
		t.Fatal("changed review accepted", err)
	}
	if _, err = f.accept(ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Claim(ctx, item.ID, input.ClaimToken); !errors.Is(err, ErrWaiting) {
		t.Fatal("unallocated target received token", err)
	}
	f.binding.Accepted = true
	f.binding.ResourceID = "provider-machine"
	f.allocate(t)
	first, err := f.m.Claim(ctx, item.ID, input.ClaimToken)
	if err != nil || first.Token == "" {
		t.Fatal(first, err)
	}
	reopened := &Manager{Store: f.data, Vault: f.m.Vault, ControllerURL: f.m.ControllerURL, ArtifactRoot: f.m.ArtifactRoot, Now: f.m.Now, ResolveTarget: f.m.ResolveTarget}
	retry, err := reopened.Claim(ctx, item.ID, input.ClaimToken)
	if err != nil || retry.Token != first.Token {
		t.Fatal("lost response rotated enrollment", retry, err)
	}
	f.now = f.now.Add(16 * time.Minute)
	fresh, err := reopened.Claim(ctx, item.ID, input.ClaimToken)
	if err != nil || fresh.Token == first.Token {
		t.Fatal("expired enrollment did not refresh", fresh, err)
	}
	public := base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	if _, err = edge.Enroll(ctx, f.data, item.NodeID, first.Token, public, f.now); err == nil {
		t.Fatal("retired enrollment token accepted")
	}
	if _, err = edge.Enroll(ctx, f.data, item.NodeID, fresh.Token, public, f.now); err != nil {
		t.Fatal(err)
	}
	enrolled, err := reopened.Claim(ctx, item.ID, input.ClaimToken)
	if err != nil || enrolled.State != "enrolled" || enrolled.Token != "" {
		t.Fatal("enrolled key was rotated", enrolled, err)
	}
}
func TestBootstrapReadinessRequiresReviewedArtifactAndFreshRuntimeEvidence(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	item, _ := f.prepare(t)
	input, _ := f.m.inputs(item)
	f.binding.Accepted = true
	f.binding.ResourceID = "machine"
	_, _ = f.accept(ctx, item)
	f.allocate(t)
	claim, err := f.m.Claim(ctx, item.ID, input.ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	if _, err = edge.Enroll(ctx, f.data, item.NodeID, claim.Token, public, f.now); err != nil {
		t.Fatal(err)
	}
	node, _ := f.data.GetPrivateNetwork(ctx, item.NodeID)
	node.State = "ready"
	node.LastVerifiedAt = &f.now
	node.Details = map[string]string{"runtimeVersion": remoteruntime.APIVersion, "runtimeCapabilities": "deploy,inspect"}
	if err = f.data.UpdatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	checked, err := f.m.Refresh(ctx, item.ID)
	if err != nil || checked.State == "ready" {
		t.Fatal("generic heartbeat marked target ready", checked, err)
	}
	f.now = f.now.Add(time.Second)
	node.Details["runtimeCheckedAt"] = f.now.Format(time.RFC3339Nano)
	node.Details["agentArtifactSHA256"] = "old-artifact"
	_ = f.data.UpdatePrivateNetwork(ctx, node)
	checked, err = f.m.Refresh(ctx, item.ID)
	if err != nil || checked.State == "ready" {
		t.Fatal("old agent artifact marked upgrade complete", checked, err)
	}
	node.Details["agentArtifactSHA256"] = item.Plan.ArtifactSHA256
	_ = f.data.UpdatePrivateNetwork(ctx, node)
	checked, err = f.m.Refresh(ctx, item.ID)
	if err != nil || checked.State != "ready" || checked.InstallationState != "installed" || checked.EnrollmentState != "enrolled" {
		t.Fatal("verified runtime did not become ready", checked, err)
	}
	saved, err := f.data.GetTargetBootstrap(ctx, item.ID)
	if err != nil || saved.EncryptedInput != "" || saved.ClaimHash != "" {
		t.Fatal("completed installation retained private inputs", err)
	}
}
func TestSSHReviewRequiresVerifiedKeyAndApprovalBeforeExecution(t *testing.T) {
	f := setup(t)
	f.binding.ProviderID = ""
	f.binding.ReviewID = ""
	f.binding.ProjectID = ""
	f.binding.Accepted = true
	f.binding.ResourceID = "target"
	ctx := context.Background()
	key, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	plan := core.TargetBootstrapPlan{Method: "ssh", Platform: "linux-amd64", ImageFamily: "existing-systemd", SSHHost: "192.0.2.10", SSHUser: "root", SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
	if _, _, err := f.m.Prepare(ctx, f.binding, plan, SSHCredentials{Password: "write-only-password"}, "owner"); err == nil {
		t.Fatal("unverified scan key accepted")
	}
	plan.SSHVerified = true
	item, _, err := f.m.Prepare(ctx, f.binding, plan, SSHCredentials{Password: "write-only-password"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	f.m.SSHInstall = func(_ context.Context, r core.TargetBootstrap, c SSHCredentials, script string) error {
		called++
		if c.Password != "write-only-password" || strings.Contains(script, c.Password) || !strings.Contains(script, "sha256") {
			t.Fatal("invalid protected installer inputs")
		}
		return nil
	}
	if _, err = f.m.InstallSSH(ctx, item.ID, item.Digest); err == nil || called != 0 {
		t.Fatal("installation ran before approval")
	}
	if _, err = f.m.Accept(ctx, item.ID, item.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.InstallSSH(ctx, item.ID, item.Digest); err != nil || called != 1 {
		t.Fatal(err, called)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "write-only-password") {
		t.Fatal("SSH credential returned through review")
	}
}

func TestBootstrapUpgradePreservesIdentityAndReplacementRetiresIt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.binding.ProviderID = ""
	f.binding.ProjectID = ""
	f.binding.ReviewID = ""
	f.binding.Accepted = true
	f.binding.ResourceID = "target"
	key, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	plan := core.TargetBootstrapPlan{TargetName: "Target", Method: "ssh", Platform: "linux-amd64", ImageFamily: "existing-systemd", SSHHost: "192.0.2.10", SSHUser: "root", SSHVerified: true, SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}
	first, _, err := f.m.Prepare(ctx, f.binding, plan, SSHCredentials{Password: "private-password"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Accept(ctx, first.ID, first.Digest); err != nil {
		t.Fatal(err)
	}
	input, _ := f.m.inputs(first)
	claim, err := f.m.Claim(ctx, first.ID, input.ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	if _, err = edge.Enroll(ctx, f.data, first.NodeID, claim.Token, public, f.now); err != nil {
		t.Fatal(err)
	}
	old, _ := f.data.GetEdgeCredential(ctx, first.NodeID)
	upgrade, _, err := f.m.Prepare(ctx, f.binding, plan, SSHCredentials{Password: "private-password"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Accept(ctx, upgrade.ID, upgrade.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Claim(ctx, first.ID, input.ClaimToken); !errors.Is(err, ErrCredential) {
		t.Fatal("superseded installer still issued credentials", err)
	}
	input, _ = f.m.inputs(upgrade)
	enrolled, err := f.m.Claim(ctx, upgrade.ID, input.ClaimToken)
	if err != nil || enrolled.Token != "" || enrolled.State != "enrolled" {
		t.Fatal(enrolled, err)
	}
	current, _ := f.data.GetEdgeCredential(ctx, first.NodeID)
	if current.Generation != old.Generation || current.PublicKey != old.PublicKey {
		t.Fatal("upgrade replaced identity")
	}
	f.now = f.now.Add(time.Second)
	node, _ := f.data.GetPrivateNetwork(ctx, upgrade.NodeID)
	node.Details["runtimeVersion"] = remoteruntime.APIVersion
	node.Details["runtimeCapabilities"] = "deploy"
	node.Details["runtimeCheckedAt"] = f.now.Format(time.RFC3339Nano)
	node.Details["agentArtifactSHA256"] = upgrade.Plan.ArtifactSHA256
	if err = f.data.UpdatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if refreshed, e := f.m.Refresh(ctx, upgrade.ID); e != nil || refreshed.State == "ready" {
		t.Fatal("old agent heartbeat completed an unexecuted SSH reinstall", refreshed, e)
	}
	plan.ReplaceIdentity = true
	replacement, _, err := f.m.Prepare(ctx, f.binding, plan, SSHCredentials{Password: "private-password"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Accept(ctx, replacement.ID, replacement.Digest); err != nil {
		t.Fatal(err)
	}
	input, _ = f.m.inputs(replacement)
	fresh, err := f.m.Claim(ctx, replacement.ID, input.ClaimToken)
	if err != nil || fresh.Token == "" {
		t.Fatal(fresh, err)
	}
	current, _ = f.data.GetEdgeCredential(ctx, first.NodeID)
	if current.Generation != old.Generation+1 || current.PublicKey != "" || current.SessionHash != "" {
		t.Fatal("replacement did not retire old identity")
	}
	if _, err = edge.Enroll(ctx, f.data, first.NodeID, claim.Token, public, f.now); err == nil {
		t.Fatal("old token survived identity replacement")
	}
}

func TestSSHInterruptedInstallationRetriesSameReceipt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.binding.ProviderID = ""
	f.binding.ProjectID = ""
	f.binding.ReviewID = ""
	f.binding.Accepted = true
	f.binding.ResourceID = "target"
	key, _ := ssh.NewPublicKey(ed25519.NewKeyFromSeed(make([]byte, 32)).Public())
	item, _, err := f.m.Prepare(ctx, f.binding, core.TargetBootstrapPlan{TargetName: "Target", Method: "ssh", Platform: "linux-amd64", ImageFamily: "existing-systemd", SSHHost: "192.0.2.10", SSHUser: "root", SSHVerified: true, SSHHostKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))}, SSHCredentials{Password: "private-password"}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Accept(ctx, item.ID, item.Digest); err != nil {
		t.Fatal(err)
	}
	calls := 0
	f.m.SSHInstall = func(_ context.Context, got core.TargetBootstrap, _ SSHCredentials, _ string) error {
		calls++
		if got.ID != item.ID || got.ServerID != item.ServerID {
			t.Fatal("retry changed installation")
		}
		if calls == 1 {
			return errors.New("upstream leaked-password output")
		}
		return nil
	}
	failed, err := f.m.InstallSSH(ctx, item.ID, item.Digest)
	if err == nil || failed.State != "unknown" || strings.Contains(failed.Message, "leaked-password") {
		t.Fatal(failed, err)
	}
	if _, err = f.m.Retry(ctx, item.ID, "changed"); err == nil {
		t.Fatal("changed retry plan accepted")
	}
	if _, err = f.m.Retry(ctx, item.ID, item.Digest); err != nil {
		t.Fatal(err)
	}
	done, err := f.m.InstallSSH(ctx, item.ID, item.Digest)
	if err != nil || done.InstallationState != "installed" || calls != 2 {
		t.Fatal(done, err, calls)
	}
	rows, err := f.data.ListTargetBootstraps(ctx, item.ServerID)
	if err != nil || len(rows) != 1 {
		t.Fatal("retry allocated another operation", err)
	}
}

func TestBootstrapIssuanceRechecksProviderResourceWithinTransaction(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	item, _ := f.prepare(t)
	if _, err := f.accept(ctx, item); err != nil {
		t.Fatal(err)
	}
	f.binding.Accepted = true
	f.binding.ResourceID = "machine"
	f.allocate(t)
	// Simulate ownership changing after the manager's resolution. The store must
	// reject issuance even though the injected resolver still reports allocation.
	machine, err := f.data.GetManagedServer(ctx, item.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	machine.AllocationState = "deleted"
	machine.Revision++
	if err = f.data.UpdateManagedServer(ctx, machine, machine.Revision-1); err != nil {
		t.Fatal(err)
	}
	input, _ := f.m.inputs(item)
	if _, err = f.m.Claim(ctx, item.ID, input.ClaimToken); err == nil {
		t.Fatal("deleted provider target obtained enrollment")
	}
	if _, err = f.data.GetEdgeCredential(ctx, item.NodeID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed issuance left a credential", err)
	}
}

func TestBootstrapMissingUserDataReportsRecoveryAndExpiresPrivateInputs(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	item, _ := f.prepare(t)
	if _, err := f.accept(ctx, item); err != nil {
		t.Fatal(err)
	}
	f.binding.Accepted = true
	f.binding.ResourceID = "machine"
	f.allocate(t)
	f.now = f.now.Add(31 * time.Minute)
	missing, err := f.m.Refresh(ctx, item.ID)
	if err != nil || missing.State != "unknown" || missing.InstallationState != "interrupted" {
		t.Fatal("silent user-data failure", missing, err)
	}
	operations, err := f.data.ListInfrastructureOperations(ctx, item.ServerID)
	if err != nil || len(operations) != 1 {
		t.Fatal("installation recovery changed allocation", err)
	}
	f.now = f.now.Add(24 * time.Hour)
	expired, err := f.m.Refresh(ctx, item.ID)
	if err != nil || expired.State != "failed" {
		t.Fatal(expired, err)
	}
	saved, err := f.data.GetTargetBootstrap(ctx, item.ID)
	if err != nil || saved.EncryptedInput != "" || saved.ClaimHash != "" {
		t.Fatal("expired installation retained credentials", err)
	}
}

func TestExpiredBootstrapReviewErasesPrivateInputWithoutInstalling(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	item, _ := f.prepare(t)
	f.now = f.now.Add(16 * time.Minute)
	expired, err := f.m.Refresh(ctx, item.ID)
	if err != nil || expired.State != "failed" || expired.EncryptedInput != "" || expired.ClaimHash != "" {
		t.Fatal(expired, err)
	}
	if _, err = f.m.Accept(ctx, item.ID, item.Digest); err == nil {
		t.Fatal("expired installation review accepted")
	}
	if _, err = f.data.GetEdgeCredential(ctx, item.NodeID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("expired review created identity", err)
	}
}

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
	f.m = &Manager{Store: data, Vault: vault, ControllerURL: "https://dispatch.example.com", ArtifactRoot: root, Now: func() time.Time { return f.now }, ResolveTarget: func(context.Context, string) (Binding, error) { return f.binding, nil }}
	return f
}
func (f *fixture) prepare(t *testing.T) (core.TargetBootstrap, string) {
	t.Helper()
	item, rendered, err := f.m.Prepare(context.Background(), f.binding, core.TargetBootstrapPlan{Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true, Method: "cloud_init"}, SSHCredentials{}, "owner")
	if err != nil {
		t.Fatal(err)
	}
	return item, rendered
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
	if _, err = f.m.Accept(ctx, item.ID, item.Digest); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Claim(ctx, item.ID, input.ClaimToken); !errors.Is(err, ErrWaiting) {
		t.Fatal("unallocated target received token", err)
	}
	f.binding.Accepted = true
	f.binding.ResourceID = "provider-machine"
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
	_, _ = f.m.Accept(ctx, item.ID, item.Digest)
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

package provision

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func snapshotFixture(t *testing.T) (*lifecycleFixture, core.ManagedServer) {
	t.Helper()
	f := newLifecycleFixture(t)
	ctx := context.Background()
	p, _ := f.data.GetInfrastructureProvider(ctx, f.input.ProviderID)
	caps := append(append([]string{}, p.Capabilities...), provider.CapabilitySnapshotCreate, provider.CapabilitySnapshotInspect, provider.CapabilitySnapshotDelete, provider.CapabilityRestore)
	if _, err := f.m.Update(ctx, p.ID, Registration{Name: p.Name, Endpoint: p.Endpoint, Enabled: true, Revision: p.Revision, Capabilities: caps}); err != nil {
		t.Fatal(err)
	}
	policy := core.InfrastructureQuotaPolicy{ProjectID: "project", Revision: 1, MaxServers: 5, MaxSnapshots: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, AnyRegion: true, AnySize: true}}, UpdatedAt: f.now}
	if err := f.data.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	f.m.Admission = f.data.InfrastructureQuotaAdmission
	_, accepted := f.accept(t)
	if op := f.finish(t, accepted.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	source, _ := f.data.GetManagedServer(ctx, accepted.Server.ID)
	return f, source
}
func captureReview(t *testing.T, f *lifecycleFixture, source core.ManagedServer) core.InfrastructureSnapshotReview {
	t.Helper()
	r, err := f.m.ReviewSnapshot(context.Background(), source.ID, SnapshotInput{Name: "retained disks", DiskSet: "all", Consistency: provider.ConsistencyCrash, Encryption: provider.SnapshotEncryption{Mode: "provider-managed"}, RetainUntil: f.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func acceptCapture(t *testing.T, f *lifecycleFixture, r core.InfrastructureSnapshotReview, key string) AcceptedSnapshot {
	t.Helper()
	accepted, err := f.m.AcceptSnapshot(context.Background(), "actor", Acceptance{ReviewID: r.ID, Digest: r.Digest, ConfirmName: r.Name, RequestKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
func TestSnapshotLifecycleRestartReplayQuotaAndRetention(t *testing.T) {
	f, source := snapshotFixture(t)
	ctx := context.Background()
	review := captureReview(t, f, source)
	accepted := acceptCapture(t, f, review, "capture-key")
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			again, err := f.m.AcceptSnapshot(ctx, "actor", Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "capture-key"})
			if err != nil || again.Operation.ID != accepted.Operation.ID {
				t.Errorf("replay %v", err)
			}
		}()
	}
	wg.Wait()
	second := captureReview(t, f, source)
	_, err := f.m.AcceptSnapshot(ctx, "actor", Acceptance{ReviewID: second.ID, Digest: second.Digest, ConfirmName: second.Name, RequestKey: "another-snapshot"})
	var quota *core.InfrastructureQuotaViolation
	if !errors.As(err, &quota) || quota.Limit != "maxSnapshots" {
		t.Fatal("pending capture escaped quota", err)
	}
	lost := false
	f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if !lost && r.Method == "POST" && r.URL.Path == "/v1/snapshots" && err == nil {
			lost = true
			response.Body.Close()
			return nil, errors.New("reply lost after capture acceptance")
		}
		return response, err
	})}
	f.tick(t)
	reopened, err := store.Open(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	f.data = reopened
	f.m.Store = reopened
	f.m.Admission = reopened.InfrastructureQuotaAdmission
	f.m.Secrets = secretvalue.New(reopened, f.m.Vault)
	f.adapter, err = mock.New(mock.Options{StateFile: f.mockPath})
	if err != nil {
		t.Fatal(err)
	}
	if op := f.finish(t, accepted.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	saved, err := f.data.GetInfrastructureSnapshot(ctx, accepted.Snapshot.ID)
	if err != nil || saved.State != "ready" {
		t.Fatal(saved, err)
	}
	items, _ := f.adapter.Snapshots(ctx, source.ResourceID)
	if len(items) != 1 {
		t.Fatal("lost response allocated duplicate snapshots")
	}
	current, _ := f.data.GetManagedServer(ctx, source.ID)
	if current.ResourceID != source.ResourceID || current.AllocationState != "allocated" {
		t.Fatal("capture changed source")
	}
	deletion, err := f.m.ReviewDelete(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.m.Delete(ctx, source.ID, "actor", Acceptance{Digest: deletion.Digest, ConfirmName: source.Name, RequestKey: "delete-source"})
	if err != nil {
		t.Fatal(err)
	}
	if op := f.finish(t, removed.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	if _, err = f.adapter.Snapshot(ctx, saved.ResourceID); err != nil {
		t.Fatal("source deletion removed snapshot", err)
	}
	dr, err := f.m.ReviewSnapshotDelete(ctx, saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	deleting := acceptCapture(t, f, dr, "delete-snapshot")
	if op := f.finish(t, deleting.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	saved, _ = f.data.GetInfrastructureSnapshot(ctx, saved.ID)
	if saved.State != "deleted" {
		t.Fatal(saved)
	}
}
func TestSnapshotCancellationBeforeAndAfterSubmission(t *testing.T) {
	for _, submitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "uncertain"}[submitted], func(t *testing.T) {
			f, source := snapshotFixture(t)
			ctx := context.Background()
			review := captureReview(t, f, source)
			accepted := acceptCapture(t, f, review, "cancel-capture")
			if submitted {
				f.m.HTTPClient = &http.Client{Transport: upgradeTransport(func(r *http.Request) (*http.Response, error) {
					response, err := http.DefaultTransport.RoundTrip(r)
					if r.Method == "POST" && r.URL.Path == "/v1/snapshots" && err == nil {
						response.Body.Close()
						return nil, errors.New("lost acceptance")
					}
					return response, err
				})}
				f.tick(t)
			}
			if err := f.m.ChangeOperation(ctx, accepted.Operation.ID, "cancel"); err != nil {
				t.Fatal(err)
			}
			op := f.finish(t, accepted.Operation.ID)
			want := "cancelled"
			if submitted {
				want = "unknown"
			}
			if op.State != want {
				t.Fatal(op)
			}
			saved, _ := f.data.GetInfrastructureSnapshot(ctx, accepted.Snapshot.ID)
			second := captureReview(t, f, source)
			_, err := f.m.AcceptSnapshot(ctx, "actor", Acceptance{ReviewID: second.ID, Digest: second.Digest, ConfirmName: second.Name, RequestKey: "second-capture"})
			if !submitted {
				if err != nil {
					t.Fatal("pre-submission cancellation kept quota", err)
				}
				return
			}
			var quota *core.InfrastructureQuotaViolation
			if !errors.As(err, &quota) {
				t.Fatal("uncertain allocation released quota", err)
			}
			request, err := f.m.snapshotRequest(review)
			if err != nil {
				t.Fatal(err)
			}
			remote, err := f.adapter.CreateSnapshot(ctx, op.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				remote, _ = f.adapter.Operation(ctx, remote.ID)
			}
			f.m.HTTPClient = nil
			resolved, err := f.m.ResolveSnapshot(ctx, saved.ID, Adoption{ResourceID: remote.ResourceID, Revision: saved.Revision, ConfirmName: saved.Name})
			if err != nil || resolved.State != "ready" {
				t.Fatal(resolved, err)
			}
			current, _ := f.data.GetManagedServer(ctx, source.ID)
			if current.AllocationState != "allocated" {
				t.Fatal("snapshot resolution changed source")
			}
		})
	}
}
func TestSnapshotRetentionRejectsUnsupportedConsistencyBeforeMutation(t *testing.T) {
	f, source := snapshotFixture(t)
	ctx := context.Background()
	_, err := f.m.ReviewSnapshot(ctx, source.ID, SnapshotInput{Name: "application", DiskSet: "all", Consistency: provider.ConsistencyApplication, Encryption: provider.SnapshotEncryption{Mode: "provider-managed"}})
	if err == nil {
		t.Fatal("application consistency accepted without quiescing")
	}
	r, err := f.m.ReviewSnapshot(ctx, source.ID, SnapshotInput{Name: "protected", DiskSet: "boot", Consistency: provider.ConsistencyCrash, Encryption: provider.SnapshotEncryption{Mode: "provider-managed"}})
	if err != nil {
		t.Fatal(err)
	}
	accepted := acceptCapture(t, f, r, "protected-capture")
	f.finish(t, accepted.Operation.ID)
	if _, err = f.m.ReviewSnapshotDelete(ctx, accepted.Snapshot.ID); !errors.Is(err, store.ErrSnapshotProtected) {
		t.Fatal("retention ignored", err)
	}
}
func TestSnapshotRestoreUsesFreshBootstrapAndRemainsIsolated(t *testing.T) {
	f, source := snapshotFixture(t)
	ctx := context.Background()
	r := captureReview(t, f, source)
	captured := acceptCapture(t, f, r, "clone-capture")
	f.finish(t, captured.Operation.ID)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("pinned clone agent"), 0600); err != nil {
		t.Fatal(err)
	}
	f.m.Bootstrap = bootstrap.Configured(f.data, f.m.Vault, "https://dispatch.example.com")
	f.m.Bootstrap.ArtifactRoot = root
	f.m.Bootstrap.Now = f.m.Now
	in := f.input
	in.ActorID = "actor"
	in.SourceSnapshotID = captured.Snapshot.ID
	in.Name = "isolated clone"
	in.Network = "mock-isolated"
	in.ActorID = "actor"
	in.Bootstrap = &core.TargetBootstrapPlan{Method: "cloud_init", Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true}
	review, err := f.m.ReviewCreate(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "clone-request"})
	if err != nil {
		t.Fatal(err)
	}
	if clone.Server.NodeID == source.NodeID || clone.Server.ID == source.ID || clone.Server.BootstrapID == "" || clone.Server.SourceSnapshotID != captured.Snapshot.ID {
		t.Fatal("clone reused source binding")
	}
	deletion, err := f.m.ReviewSnapshotDelete(ctx, captured.Snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.AcceptSnapshot(ctx, "actor", Acceptance{ReviewID: deletion.ID, Digest: deletion.Digest, ConfirmName: deletion.Name, RequestKey: "delete-during-clone"}); !errors.Is(err, store.ErrSnapshotProtected) {
		t.Fatal("snapshot deleted during materialization", err)
	}
	if op := f.finish(t, clone.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	if err = f.m.RefreshReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.data.GetServer(ctx, clone.Server.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("clone published before bootstrap")
	}
	boot, err := f.data.GetTargetBootstrap(ctx, clone.Server.BootstrapID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := f.m.Vault.Decrypt("target-bootstrap:"+boot.ID, boot.EncryptedInput)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		ClaimToken string `json:"claimToken"`
	}
	if json.Unmarshal(raw, &saved) != nil {
		t.Fatal("claim missing")
	}
	claim, err := f.m.Bootstrap.Claim(ctx, boot.ID, saved.ClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	public := base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey))
	if _, err = edge.Enroll(ctx, f.data, boot.NodeID, claim.Token, public, f.now); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	node, _ := f.data.GetPrivateNetwork(ctx, boot.NodeID)
	node.Details = map[string]string{"runtimeVersion": remoteruntime.APIVersion, "runtimeCapabilities": "deploy,inspect", "runtimeCheckedAt": f.now.Format(time.RFC3339Nano), "agentArtifactSHA256": boot.Plan.ArtifactSHA256}
	node.LastVerifiedAt = &f.now
	if err = f.data.UpdatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err = f.m.RefreshReadiness(ctx); err != nil {
		t.Fatal(err)
	}
	machine, _ := f.data.GetManagedServer(ctx, clone.Server.ID)
	if machine.RuntimeState != "verified-isolated" {
		t.Fatal(machine)
	}
	if _, err = f.data.GetServer(ctx, machine.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("isolated clone published as production workload target")
	}
	sourceAfter, _ := f.data.GetManagedServer(ctx, source.ID)
	if sourceAfter.ResourceID != source.ResourceID || sourceAfter.NodeID != source.NodeID {
		t.Fatal("source identity changed")
	}
	raw, _ = json.Marshal(review)
	if strings.Contains(string(raw), "#cloud-config") || strings.Contains(string(raw), saved.ClaimToken) {
		t.Fatal("clone review leaked bootstrap")
	}
	dr, err := f.m.ReviewDelete(ctx, machine.ID)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := f.m.Delete(ctx, machine.ID, "actor", Acceptance{Digest: dr.Digest, ConfirmName: machine.Name, RequestKey: "cleanup-clone"})
	if err != nil {
		t.Fatal(err)
	}
	if op := f.finish(t, removed.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	retained, _ := f.data.GetInfrastructureSnapshot(ctx, captured.Snapshot.ID)
	if retained.State != "ready" {
		t.Fatal("clone deletion removed retained snapshot")
	}
}
func TestSnapshotUnsafeCloneNeverPublishesOrReleasesQuota(t *testing.T) {
	f, source := snapshotFixture(t)
	ctx := context.Background()
	review := captureReview(t, f, source)
	captured := acceptCapture(t, f, review, "unsafe-capture")
	f.finish(t, captured.Operation.ID)
	f.adapter, _ = mock.New(mock.Options{StateFile: f.mockPath, UnsafeRestore: true})
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("agent"), 0600)
	f.m.Bootstrap = bootstrap.Configured(f.data, f.m.Vault, "https://dispatch.example.com")
	f.m.Bootstrap.ArtifactRoot = root
	f.m.Bootstrap.Now = f.m.Now
	in := f.input
	in.ActorID = "actor"
	in.SourceSnapshotID = captured.Snapshot.ID
	in.Network = "mock-isolated"
	in.Bootstrap = &core.TargetBootstrapPlan{Method: "cloud_init", Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true}
	r, err := f.m.ReviewCreate(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: r.ID, Digest: r.Digest, ConfirmName: r.Name, RequestKey: "unsafe-clone"})
	if err != nil {
		t.Fatal(err)
	}
	op := f.finish(t, clone.Operation.ID)
	if op.State != "unknown" || op.ErrorCode != "restore_evidence" {
		t.Fatal(op)
	}
	rows, _ := f.data.ListInfrastructureQuotaReservations(ctx, "project")
	for _, row := range rows {
		if row.ServerID == clone.Server.ID && row.State != "unknown" {
			t.Fatal("unsafe clone released reservation")
		}
	}
}

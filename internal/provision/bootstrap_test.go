package provision

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
)

func TestProviderBootstrapIsBoundToAcceptedReviewAndSavedRequest(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("pinned bootstrap agent"), 0600); err != nil {
		t.Fatal(err)
	}
	f.m.Bootstrap = bootstrap.Configured(f.data, f.m.Vault, "https://dispatch.example.com")
	f.m.Bootstrap.ArtifactRoot = root
	f.m.Bootstrap.Now = f.m.Now
	f.input.ActorID = "owner"
	f.input.Bootstrap = &core.TargetBootstrapPlan{Method: "cloud_init", Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true}
	review, err := f.m.ReviewCreate(ctx, f.input)
	if err != nil {
		t.Fatal(err)
	}
	item, err := f.data.GetTargetBootstrap(ctx, review.BootstrapID)
	if err != nil {
		t.Fatal(err)
	}
	if item.AcceptedAt != nil || item.ServerID != review.ServerID || item.ProviderID != review.ProviderID || item.ProjectID != review.ProjectID || item.NodeID != "node-"+review.ServerID {
		t.Fatal("bootstrap binding escaped review", item)
	}
	raw, err := f.m.Vault.Decrypt("target-bootstrap:"+item.ID, item.EncryptedInput)
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		ClaimToken string `json:"claimToken"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if _, err = f.m.Bootstrap.Claim(ctx, item.ID, input.ClaimToken); err == nil {
		t.Fatal("unaccepted review released token")
	}
	before, err := f.m.reviewedRequest(review)
	if err != nil || !strings.HasPrefix(before.Bootstrap, "#cloud-config") {
		t.Fatal("missing exact cloud-init", err)
	}
	public, _ := json.Marshal(review)
	if strings.Contains(string(public), input.ClaimToken) || strings.Contains(string(public), "#cloud-config") {
		t.Fatal("claim escaped public review")
	}
	accepted, err := f.m.AcceptCreate(ctx, "owner", Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "approved-bootstrap"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = f.data.GetTargetBootstrap(ctx, review.BootstrapID)
	if err != nil || item.AcceptedAt == nil || accepted.Server.BootstrapID != item.ID {
		t.Fatal("bootstrap not atomically accepted", err)
	}
	if _, err = f.m.Bootstrap.Claim(ctx, item.ID, input.ClaimToken); err == nil {
		t.Fatal("unallocated review released token")
	}
	for i := 0; i < 4; i++ {
		f.now = f.now.Add(time.Second)
		if _, err = f.m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	machine, err := f.data.GetManagedServer(ctx, review.ServerID)
	if err != nil || machine.AllocationState != "allocated" {
		t.Fatal(machine, err)
	}
	after, err := f.m.reviewedRequest(review)
	if err != nil || after.Bootstrap != before.Bootstrap {
		t.Fatal("bootstrap changed after provider acceptance", err)
	}
	if _, err = f.m.Enrollment(ctx, machine.ID); err == nil {
		t.Fatal("manual enrollment bypassed bootstrap")
	}
	f.now = f.now.Add(18 * time.Minute)
	claim, err := f.m.Bootstrap.Claim(ctx, item.ID, input.ClaimToken)
	if err != nil || claim.Token == "" || !claim.ExpiresAt.After(f.now.Add(14*time.Minute)) {
		t.Fatal("slow allocation did not receive fresh enrollment", claim, err)
	}
	if len(f.requests) != 1 {
		t.Fatal("bootstrap requested duplicate machine", len(f.requests))
	}
	// A second accepted review cannot reuse the first target's pre-rendered claim.
	forged := review
	forged.ID = "other-review"
	forged.ServerID = "other-server"
	forged.State = "open"
	forged.ExpiresAt = f.now.Add(time.Minute)
	if err = f.data.CreateInfrastructureReview(ctx, forged); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.data.AcceptInfrastructureReview(ctx, forged.ID, forged.Digest, "other-operation", "owner", f.now, nil); err == nil {
		t.Fatal("foreign review reused bootstrap")
	}
}

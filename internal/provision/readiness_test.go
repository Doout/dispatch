package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/bootstrap"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/remoteruntime"
)

func TestManagedServerReadinessRefreshesOnlyAuthorizedServer(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	if op := f.finish(t, accepted.Operation.ID); op.State != "succeeded" {
		t.Fatal(op)
	}
	status, err := f.m.GetManaged(ctx, accepted.Server.ID)
	if err != nil || status.AllocationState != "allocated" || status.WaitState != "waiting" || status.Deployable || status.LatestOperation.ID != accepted.Operation.ID {
		t.Fatal("allocation was treated as readiness", status, err)
	}
	enrollment, err := f.m.Enrollment(ctx, status.ID)
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = edge.Enroll(ctx, f.data, enrollment.NodeID, enrollment.Token, base64.RawURLEncoding.EncodeToString(pub), f.now); err != nil {
		t.Fatal(err)
	}
	node, err := f.data.GetPrivateNetwork(ctx, enrollment.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	node.Details = map[string]string{"runtimeVersion": remoteruntime.APIVersion, "runtimeCapabilities": "deploy,inspect", "runtimeCheckedAt": f.now.Format(time.RFC3339Nano)}
	if err = f.data.UpdatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	status, err = f.m.GetManaged(ctx, status.ID)
	if err != nil || status.WaitState != "ready" || !status.Deployable {
		t.Fatal("fresh authenticated runtime was not ready", status, err)
	}

	// Another stale server stays unchanged when this server is inspected.
	f.input.Name = "unrelated"
	review, err := f.m.ReviewCreate(ctx, f.input)
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.m.AcceptCreate(ctx, "actor", Acceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Name, RequestKey: "unrelated-key"})
	if err != nil {
		t.Fatal(err)
	}
	other.Server.AllocationState, other.Server.EnrollmentState, other.Server.RuntimeState = "allocated", "enrolled", "ready"
	other.Server.Revision++
	if err = f.data.UpdateManagedServer(ctx, other.Server, other.Server.Revision-1); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Minute)
	denied := errors.New("access revoked")
	f.m.Authorize = func(context.Context, string, string, string) error { return denied }
	if _, err = f.m.GetManaged(ctx, status.ID); !errors.Is(err, denied) {
		t.Fatal("revoked access read readiness", err)
	}
	stored, _ := f.data.GetManagedServer(ctx, status.ID)
	if stored.RuntimeState != "ready" || stored.Revision != status.Revision {
		t.Fatal("unauthorized read changed readiness", stored)
	}
	f.m.Authorize = nil
	status, err = f.m.GetManaged(ctx, status.ID)
	if err != nil || status.WaitState != "waiting" || status.Deployable || status.RuntimeState != "waiting" {
		t.Fatal("stale runtime evidence remained deployable", status, err)
	}
	stored, _ = f.data.GetManagedServer(ctx, other.Server.ID)
	if stored.RuntimeState != "ready" || stored.Revision != other.Server.Revision {
		t.Fatal("inspection refreshed an unrelated server", stored)
	}
	// A timestamp too far in the future is not fresh runtime evidence.
	node.Details["runtimeCheckedAt"] = f.now.Add(time.Minute).Format(time.RFC3339Nano)
	if err = f.data.UpdatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	status, err = f.m.GetManaged(ctx, status.ID)
	if err != nil || status.Deployable {
		t.Fatal("future runtime evidence became deployable", status, err)
	}
	if err = f.data.RevokeEdgeCredential(ctx, status.NodeID, f.now); err != nil {
		t.Fatal(err)
	}
	status, err = f.m.GetManaged(ctx, status.ID)
	if err != nil || status.WaitState != "revoked" || status.EnrollmentState != "revoked" || status.Deployable {
		t.Fatal("revoked identity remained ready", status, err)
	}
}

func TestManagedServerReadinessStopsForExpiredEnrollmentAndBootstrap(t *testing.T) {
	t.Run("enrollment", func(t *testing.T) {
		f := newLifecycleFixture(t)
		ctx := context.Background()
		_, accepted := f.accept(t)
		f.finish(t, accepted.Operation.ID)
		if _, err := f.m.Enrollment(ctx, accepted.Server.ID); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(edge.EnrollmentLifetime + time.Minute)
		status, err := f.m.GetManaged(ctx, accepted.Server.ID)
		if err != nil || status.WaitState != "expired" || status.EnrollmentState != "expired" || status.Deployable {
			t.Fatal("expired enrollment kept waiting", status, err)
		}
	})
	t.Run("bootstrap", func(t *testing.T) {
		f := newLifecycleFixture(t)
		ctx := context.Background()
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "linux-amd64"), []byte("pinned fixture"), 0600); err != nil {
			t.Fatal(err)
		}
		f.m.Bootstrap = bootstrap.Configured(f.data, f.m.Vault, "https://dispatch.example.com")
		f.m.Bootstrap.ArtifactRoot, f.m.Bootstrap.Now = root, f.m.Now
		f.input.ActorID = "owner"
		f.input.Bootstrap = &core.TargetBootstrapPlan{Method: "cloud_init", Platform: "linux-amd64", ImageFamily: "ubuntu-24.04", InstallRuntime: true}
		_, accepted := f.accept(t)
		f.finish(t, accepted.Operation.ID)
		f.now = f.now.Add(31 * time.Minute)
		status, err := f.m.GetManaged(ctx, accepted.Server.ID)
		if err != nil || status.WaitState != "unknown" || status.Bootstrap == nil || status.Bootstrap.InstallationState != "interrupted" || status.Deployable {
			t.Fatal("interrupted installer kept waiting", status, err)
		}
		serialized, _ := json.Marshal(status)
		for _, private := range []string{"plan", "controllerUrl", "artifactUrl", "digest", "claimHash", "encryptedInput"} {
			if strings.Contains(string(serialized), `"`+private+`"`) {
				t.Fatal("installation details escaped summary", string(serialized))
			}
		}
		f.now = f.now.Add(24 * time.Hour)
		status, err = f.m.GetManaged(ctx, accepted.Server.ID)
		if err != nil || status.WaitState != "failed" || status.Bootstrap.State != "failed" || status.Deployable {
			t.Fatal("expired installer kept waiting", status, err)
		}
	})
}

func TestManagedServerReadinessPausedTransportNeedsExplicitRecovery(t *testing.T) {
	f := newLifecycleFixture(t)
	ctx := context.Background()
	_, accepted := f.accept(t)
	f.endpoint.Close()
	for range 10 {
		f.now = f.now.Add(2 * time.Minute)
		if _, err := f.m.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		op, err := f.data.GetInfrastructureOperation(ctx, accepted.Operation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if op.State == "paused" {
			status, err := f.m.GetManaged(ctx, accepted.Server.ID)
			if err != nil || status.WaitState != "paused" || status.Deployable || status.LatestOperation.ID != op.ID || status.LatestOperation.State != "paused" {
				t.Fatal("paused transport was mistaken for approval or readiness", status, err)
			}
			return
		}
	}
	t.Fatal("transport fixture did not pause after exhausted retries")
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func workerStoreFixture(t *testing.T) (*SQLStore, core.WorkflowWorkerJob) {
	t.Helper()
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	node := core.PrivateNetwork{ID: "node", Name: "node", Driver: "dispatch_agent", Config: map[string]string{"workflowMode": "tenant"}, CreatedAt: now, UpdatedAt: now}
	if err = data.CreatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err = data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: node.ID, EnrollmentHash: "enrollment", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = data.EnrollEdgeCredential(ctx, node.ID, "enrollment", "public-key", "session", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	job := core.WorkflowWorkerJob{ID: "operation", NodeID: node.ID, Generation: 1, ProjectID: "project", ResourceID: "resource", RevisionID: "revision", Kind: "workflow", Mode: "tenant", Digest: "digest", Request: "encrypted-request", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = data.CreateWorkflowWorkerJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	return data, job
}
func TestWorkflowWorkerClaimsAreExclusiveAndBoundToEnrollment(t *testing.T) {
	data, job := workerStoreFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var count atomic.Int32
	var mu sync.Mutex
	var lease *core.WorkflowWorkerJob
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			j, err := data.LeaseWorkflowWorkerJob(ctx, job.NodeID, 1, "tenant", now)
			if err != nil {
				t.Error(err)
			}
			if j != nil {
				count.Add(1)
				mu.Lock()
				lease = j
				mu.Unlock()
			}
		}()
	}
	group.Wait()
	if count.Load() != 1 || lease == nil {
		t.Fatalf("duplicate claims: %d", count.Load())
	}
	if _, err := data.RenewWorkflowWorkerJob(ctx, "other", job.ID, lease.LeaseToken, "cipher", now); !errors.Is(err, ErrWorkerLease) {
		t.Fatal("another node renewed the lease")
	}
	if err := data.CompleteWorkflowWorkerJob(ctx, job.NodeID, job.ID, "stale", "succeeded", "result", now); !errors.Is(err, ErrWorkerLease) {
		t.Fatal("stale completion accepted")
	}
	if err := data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: job.NodeID, EnrollmentHash: "new", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := data.RenewWorkflowWorkerJob(ctx, job.NodeID, job.ID, lease.LeaseToken, "cipher", now); !errors.Is(err, ErrWorkerLease) {
		t.Fatal("old enrollment renewed the lease")
	}
	got, err := data.GetWorkflowWorkerJob(ctx, job.ID)
	if err != nil || got.State != "unknown" || got.Request != "" {
		t.Fatalf("replaced identity retained request: %+v %v", got, err)
	}
}
func TestWorkflowWorkerCancellationAndExpiryRemoveQueuedCredentials(t *testing.T) {
	data, job := workerStoreFixture(t)
	ctx := context.Background()
	if err := data.CancelWorkflowWorkerJob(ctx, job.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	got, err := data.GetWorkflowWorkerJob(ctx, job.ID)
	if err != nil || got.State != "cancelled" || got.Request != "" {
		t.Fatalf("cancelled request retained: %+v %v", got, err)
	}
	job.ID = "expired"
	job.ExpiresAt = time.Now().Add(-time.Second)
	if err = data.CreateWorkflowWorkerJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, err = data.GetWorkflowWorkerJob(ctx, job.ID)
	if err != nil || got.State != "cancelled" || got.Request != "" {
		t.Fatalf("expired request retained: %+v %v", got, err)
	}
}

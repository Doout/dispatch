package provision

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/provider"
)

// Reconcile performs at most one provider mutation per lease. The stable request
// is saved before I/O, and a returned operation identity is saved before polling.
func (m *Manager) Reconcile(ctx context.Context) (bool, error) {
	data, err := m.lifecycle()
	if err != nil {
		return false, err
	}
	now := m.now()
	op, err := data.LeaseInfrastructureOperation(ctx, now, 2*time.Minute)
	if err != nil || op == nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	if strings.HasPrefix(op.Action, "server.") {
		return true, m.reconcileMachineAction(ctx, op)
	}
	if strings.HasPrefix(op.Action, "snapshot.") {
		return true, m.reconcileSnapshot(ctx, op)
	}
	server, err := data.GetManagedServer(ctx, op.ServerID)
	if err != nil {
		return true, err
	}
	review, err := data.GetInfrastructureReview(ctx, server.ReviewID)
	if err != nil {
		return true, err
	}
	checkpoint := func() error {
		return data.CheckpointInfrastructureOperation(context.WithoutCancel(ctx), *op, true, m.now())
	}
	stop := func(code, message string) error {
		op.State = "unknown"
		op.ErrorCode = code
		op.Message = message
		server.AllocationState = "unknown"
		if op.Action == "create" && op.Stage == "submit" {
			op.State = "cancelled"
			server.AllocationState = "cancelled"
		}
		server.Revision++
		server.UpdatedAt = m.now()
		return data.CompleteInfrastructureOperation(context.WithoutCancel(ctx), server, *op, m.now(), m.Admission)
	}
	retry := func(err error) error {
		var problem *provider.Problem
		if errors.As(err, &problem) && problem.Status < 500 && problem.Status != http.StatusTooManyRequests {
			return stop("provider_evidence", "The provider rejected reconciliation. Inspect the owned resource before continuing.")
		}
		if problem == nil && !errors.Is(err, provider.ErrTransport) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			return stop("provider_evidence", "Provider identity or response evidence changed. Inspect the owned resource before continuing.")
		}
		op.Attempts++
		op.ErrorCode = "transport"
		op.Message = "Provider communication failed; the same saved operation will be reconciled."
		if op.Attempts >= 8 {
			op.State = "paused"
			op.Message = "Provider communication remains unavailable. Retry this saved operation after restoring connectivity."
		}
		op.NextAttemptAt = m.now().Add(time.Duration(1<<min(op.Attempts, 6)) * time.Second)
		return checkpoint()
	}
	// Cancellation cannot erase evidence of a request that might have reached the
	// provider. In particular it never replays create merely to discover an ID.
	if !op.ExpiresAt.After(m.now()) || op.Action == "create" && op.CancelRequested {
		if op.Action == "create" && op.Stage == "submit" {
			op.State = "cancelled"
			op.Message = "Cancelled before provider submission."
			server.AllocationState = "cancelled"
			server.Revision++
			server.UpdatedAt = m.now()
			return true, data.CompleteInfrastructureOperation(ctx, server, *op, m.now(), m.Admission)
		}
		return true, stop("cancelled_unknown", "Creation was cancelled after submission became possible. Inspect and adopt or remove the owned resource; creation will not resume.")
	}
	capability := provider.CapabilityInspect
	if op.Stage == "submit" || op.Stage == "submitting" {
		capability = provider.CapabilityCreate
		if review.SourceSnapshotID != "" {
			capability = provider.CapabilityRestore
		}
		if op.Action == "delete" {
			capability = provider.CapabilityDelete
		}
	}
	expectedManifest := review.ManifestDigest
	// Reviewed create payloads remain pinned. Inspection and deletion reference
	// durable operation/resource identities under the current approved contract.
	if op.Stage == "poll" || op.Action == "delete" {
		expectedManifest = ""
	}
	client, _, err := m.Adapter(ctx, server.ProviderID, capability, expectedManifest)
	if err != nil {
		return true, retry(err)
	}
	if op.Stage == "submit" || op.Stage == "submitting" {
		// Make submission uncertainty durable before contacting the provider.
		op.Stage = "submitting"
		op.State = "running"
		if err = data.CheckpointInfrastructureOperation(ctx, *op, false, m.now()); err != nil {
			return true, err
		}
		// Re-read cancellation after checkpoint; a cancelled unacknowledged request
		// is never replayed following a process restart.
		latest, e := data.GetInfrastructureOperation(ctx, op.ID)
		if e != nil {
			return true, e
		}
		if !latest.ExpiresAt.After(m.now()) || latest.CancelRequested && op.Action == "create" {
			return true, stop("cancelled_unknown", "Creation was cancelled at submission. Inspect the owned resource before continuing.")
		}
		var remote provider.Operation
		if op.Action == "create" {
			request, e := m.reviewedRequest(review)
			if e != nil {
				return true, stop("review_unavailable", "The saved encrypted review is unavailable; creation will not resume.")
			}
			if review.SourceSnapshotID != "" {
				snapshot, e := m.reviewedCloneSnapshot(review)
				if e != nil {
					return true, stop("restore_review", "Saved restore evidence is unavailable.")
				}
				remote, err = client.RestoreServer(ctx, op.ID, snapshot.ID, provider.RestoreServerRequest{Server: request, Policy: provider.IsolatedRestorePolicy(), ExpectedSnapshotDigest: provider.SnapshotDigest(snapshot)})
			} else {
				remote, err = client.CreateServer(ctx, op.ID, request)
			}
		} else {
			resource, e := client.Server(ctx, server.ResourceID)
			var absent *provider.Problem
			if e != nil && !(errors.As(e, &absent) && absent.Status == http.StatusNotFound) {
				return true, retry(e)
			}
			if e == nil && ownedResource(review, resource) != nil {
				return true, stop("ownership", "The provider resource does not match Dispatch ownership; deletion was stopped.")
			}
			remote, err = client.DeleteServer(ctx, op.ID, server.ResourceID)
		}
		if err != nil {
			return true, retry(err)
		}
		if !m.safeEvidence(review, remote) {
			return true, stop("private_evidence", "Provider evidence contains private configuration and cannot be recorded.")
		}
		if op.Action == "delete" && remote.ResourceID != "" && remote.ResourceID != server.ResourceID {
			return true, stop("resource_changed", "The provider returned a different deletion resource.")
		}
		op.ProviderOperationID = remote.ID
		op.ResourceID = remote.ResourceID
		op.Stage = "poll"
		op.NextAttemptAt = m.now()
		op.Attempts = 0
		return true, checkpoint()
	}
	remote, err := client.Operation(ctx, op.ProviderOperationID)
	if err != nil {
		return true, retry(err)
	}
	if !m.safeEvidence(review, remote) {
		return true, stop("private_evidence", "Provider evidence contains private configuration and cannot be recorded.")
	}
	if op.ResourceID != "" && remote.ResourceID != "" && op.ResourceID != remote.ResourceID {
		return true, stop("resource_changed", "The provider changed the resource attached to this operation.")
	}
	if remote.ResourceID != "" {
		op.ResourceID = remote.ResourceID
	}
	switch remote.State {
	case provider.StatePending, provider.StateRunning:
		op.State = "running"
		op.NextAttemptAt = m.now().Add(2 * time.Second)
		return true, checkpoint()
	case provider.StateFailed, provider.StateCancelled:
		// A failed cloud operation can leave a resource. Preserve the lock until an
		// operator verifies its disposition rather than treating failure as absence.
		return true, stop("provider_terminal", "The provider ended the operation without confirmed completion. Inspect its owned resource.")
	case provider.StateSucceeded:
		if op.Action == "create" {
			resource, e := client.Server(ctx, op.ResourceID)
			if e != nil {
				return true, retry(e)
			}
			if e = ownedResource(review, resource); e != nil || resource.State != "ready" || !m.safeEvidence(review, map[string]string{"id": resource.ID, "address": resource.Address}) {
				return true, stop("ownership", "The provider resource is not ready or does not match Dispatch ownership.")
			}
			if m.verifyReviewedClone(review, resource) != nil {
				return true, stop("restore_evidence", "Clone isolation, fresh identity or disk integrity evidence was not verified.")
			}
			server.ResourceID = resource.ID
			server.Address = resource.Address
			server.AllocationState = "allocated"
			server.PowerState = resource.PowerState
			server.PowerCheckedAt = m.now()
			server.Network = resource.Network
		} else {
			if op.ResourceID != server.ResourceID {
				return true, stop("resource_changed", "The completed deletion names a different resource.")
			}
			_, e := client.Server(ctx, server.ResourceID)
			var problem *provider.Problem
			if !errors.As(e, &problem) || problem.Status != http.StatusNotFound {
				return true, stop("deletion_unconfirmed", "The provider did not confirm resource absence after deletion.")
			}
			server.AllocationState = "deleted"
			server.RuntimeState = "removed"
			server.EnrollmentState = "revoked"
		}
		server.Revision++
		server.UpdatedAt = m.now()
		op.State = "succeeded"
		op.Message = ""
		op.ErrorCode = ""
		return true, data.CompleteInfrastructureOperation(ctx, server, *op, m.now(), m.Admission)
	}
	return true, stop("provider_evidence", "The provider returned an unsupported operation state.")
}

func (m *Manager) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for i := 0; i < 8; i++ {
				worked, err := m.Reconcile(ctx)
				if err != nil {
					logger.Warn("infrastructure reconciliation deferred")
					break
				}
				if !worked {
					break
				}
			}
			if err := m.RefreshReadiness(ctx); err != nil {
				logger.Warn("infrastructure enrollment reconciliation deferred")
			}
		}
	}
}

package provision

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
)

func (m *Manager) reconcileSnapshot(ctx context.Context, op *core.InfrastructureOperation) error {
	data, err := m.snapshots()
	if err != nil {
		return err
	}
	review, err := data.SnapshotReviewForOperation(ctx, op.ID)
	if err != nil {
		return err
	}
	snapshot, err := data.GetInfrastructureSnapshot(ctx, review.SnapshotID)
	if err != nil {
		return err
	}
	source, err := data.GetManagedServer(ctx, snapshot.ServerID)
	if err != nil {
		return err
	}
	sourceReview, err := data.GetInfrastructureReview(ctx, source.ReviewID)
	if err != nil {
		return err
	}
	checkpoint := func() error {
		return data.CheckpointInfrastructureOperation(context.WithoutCancel(ctx), *op, true, m.now())
	}
	complete := func(state, code, message string) error {
		op.State = state
		op.ErrorCode = code
		op.Message = message
		snapshot.State = "unknown"
		if snapshot.ResourceID == "" && provider.ValidID(op.ResourceID) {
			snapshot.ResourceID = op.ResourceID
		}
		if state == "cancelled" {
			snapshot.State = "cancelled"
		}
		if state == "succeeded" {
			snapshot.State = "ready"
			if op.Action == "snapshot.delete" {
				snapshot.State = "deleted"
			}
		}
		snapshot.Revision++
		snapshot.UpdatedAt = m.now()
		return data.CompleteInfrastructureSnapshot(context.WithoutCancel(ctx), snapshot, *op, m.now(), m.Admission)
	}
	stop := func(code, message string) error {
		state := "unknown"
		if op.Action == "snapshot.create" && op.Stage == "submit" {
			state = "cancelled"
		}
		return complete(state, code, message)
	}
	retry := func(err error) error {
		var p *provider.Problem
		if errors.As(err, &p) && p.Status < 500 && p.Status != 429 {
			return stop("provider_evidence", "Provider snapshot evidence was rejected; inspect the owned operation.")
		}
		if p == nil && !errors.Is(err, provider.ErrTransport) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			return stop("provider_evidence", "Provider snapshot identity or response evidence changed.")
		}
		op.Attempts++
		op.ErrorCode = "transport"
		op.Message = "Provider communication failed; the saved snapshot operation will be reconciled."
		if op.Attempts >= 8 {
			op.State = "paused"
		}
		op.NextAttemptAt = m.now().Add(time.Duration(1<<min(op.Attempts, 6)) * time.Second)
		return checkpoint()
	}
	if !op.ExpiresAt.After(m.now()) || op.CancelRequested && op.Action == "snapshot.create" {
		return stop("cancelled_unknown", "Snapshot operation stopped. A submitted request requires inspection and retains its quota.")
	}
	capability := provider.CapabilitySnapshotInspect
	expected := ""
	if op.Stage == "submit" || op.Stage == "submitting" {
		capability = provider.CapabilitySnapshotCreate
		expected = review.ManifestDigest
		if op.Action == "snapshot.delete" {
			capability = provider.CapabilitySnapshotDelete
			expected = ""
		}
	}
	client, _, err := m.Adapter(ctx, snapshot.ProviderID, capability, expected)
	if err != nil {
		return retry(err)
	}
	if op.Stage == "submit" || op.Stage == "submitting" {
		// Capture requires the same owned source and exact reviewed disk set at the
		// adapter. A source removed after review cannot silently redirect the request.
		if op.Action == "snapshot.create" {
			resource, e := client.Server(ctx, source.ResourceID)
			if e != nil {
				return retry(e)
			}
			if ownedResource(sourceReview, resource) != nil || resource.State != "ready" {
				return stop("ownership", "Snapshot source ownership changed.")
			}
		}
		if op.Action == "snapshot.delete" {
			item, e := client.Snapshot(ctx, snapshot.ResourceID)
			var absent *provider.Problem
			if e != nil && !(errors.As(e, &absent) && absent.Status == 404) {
				return retry(e)
			}
			capture, e2 := data.GetInfrastructureSnapshotReview(ctx, snapshot.ReviewID)
			if e2 != nil {
				return e2
			}
			if e == nil && ownedSnapshot(capture, item) != nil {
				return stop("ownership", "Snapshot ownership changed; deletion stopped.")
			}
		}
		op.Stage = "submitting"
		op.State = "running"
		if err = data.CheckpointInfrastructureOperation(ctx, *op, false, m.now()); err != nil {
			return err
		}
		latest, e := data.GetInfrastructureOperation(ctx, op.ID)
		if e != nil {
			return e
		}
		if !latest.ExpiresAt.After(m.now()) || latest.CancelRequested && op.Action == "snapshot.create" {
			return stop("cancelled_unknown", "Snapshot submission was cancelled; inspect before continuing.")
		}
		var remote provider.Operation
		if op.Action == "snapshot.create" {
			request, e := m.snapshotRequest(review)
			if e != nil {
				return stop("review_unavailable", "Saved snapshot review is unavailable.")
			}
			remote, err = client.CreateSnapshot(ctx, op.ID, request)
		} else {
			remote, err = client.DeleteSnapshot(ctx, op.ID, snapshot.ResourceID)
		}
		if err != nil {
			return retry(err)
		}
		if !m.safeEvidence(sourceReview, remote) {
			return stop("private_evidence", "Snapshot provider evidence contains private configuration.")
		}
		if op.Action == "snapshot.delete" && remote.ResourceID != "" && remote.ResourceID != snapshot.ResourceID {
			return stop("resource_changed", "Provider changed snapshot deletion identity.")
		}
		op.ProviderOperationID = remote.ID
		op.ResourceID = remote.ResourceID
		op.Stage = "poll"
		op.Attempts = 0
		op.NextAttemptAt = m.now()
		return checkpoint()
	}
	remote, err := client.Operation(ctx, op.ProviderOperationID)
	if err != nil {
		return retry(err)
	}
	if !m.safeEvidence(sourceReview, remote) {
		return stop("private_evidence", "Snapshot operation evidence contains private configuration.")
	}
	if op.ResourceID != "" && remote.ResourceID != "" && op.ResourceID != remote.ResourceID {
		return stop("resource_changed", "Provider changed the snapshot operation resource.")
	}
	if remote.ResourceID != "" {
		op.ResourceID = remote.ResourceID
	}
	switch remote.State {
	case provider.StatePending, provider.StateRunning:
		op.State = "running"
		op.NextAttemptAt = m.now().Add(2 * time.Second)
		return checkpoint()
	case provider.StateFailed, provider.StateCancelled:
		return stop("provider_terminal", "Snapshot operation ended without confirmed completion; inspect its owned resource.")
	case provider.StateSucceeded:
		if op.Action == "snapshot.create" {
			item, e := client.Snapshot(ctx, op.ResourceID)
			if e != nil {
				return retry(e)
			}
			request, e := m.snapshotRequest(review)
			if e != nil {
				return stop("review_unavailable", "Saved snapshot review is unavailable.")
			}
			if ownedSnapshot(review, item) != nil || item.State != "ready" || item.SourceServerID != request.SourceServerID || item.Consistency != request.Consistency || item.Encryption != request.Encryption || !snapshotDisksMatch(request, item) || !m.safeEvidence(sourceReview, item) {
				return stop("ownership", "Captured snapshot does not match its ownership, disk set or consistency review.")
			}
			item.Labels = snapshotOwnership(review)
			snapshot.Evidence, _ = json.Marshal(item)
			snapshot.ResourceID = item.ID
		} else {
			if op.ResourceID != snapshot.ResourceID {
				return stop("resource_changed", "Snapshot deletion names another resource.")
			}
			_, e := client.Snapshot(ctx, snapshot.ResourceID)
			var absent *provider.Problem
			if !errors.As(e, &absent) || absent.Status != http.StatusNotFound {
				return stop("deletion_unconfirmed", "Snapshot absence was not verified after deletion.")
			}
		}
		return complete("succeeded", "", "")
	}
	return stop("provider_evidence", "Provider returned an unsupported snapshot operation state.")
}
func snapshotDisksMatch(request provider.CreateSnapshotRequest, snapshot provider.Snapshot) bool {
	if len(request.DiskIDs) != len(snapshot.Disks) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range request.DiskIDs {
		seen[id] = true
	}
	for _, d := range snapshot.Disks {
		if !seen[d.ID] {
			return false
		}
		delete(seen, d.ID)
	}
	return len(seen) == 0
}

package provision

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type SnapshotInput struct {
	Name        string                      `json:"name"`
	DiskSet     string                      `json:"diskSet"`
	Consistency string                      `json:"consistency"`
	Encryption  provider.SnapshotEncryption `json:"encryption"`
	RetainUntil time.Time                   `json:"retainUntil"`
}
type AcceptedSnapshot struct {
	Snapshot  core.InfrastructureSnapshot  `json:"snapshot"`
	Operation core.InfrastructureOperation `json:"operation"`
}
type snapshotStore interface {
	LifecycleStore
	store.InfrastructureSnapshotStore
}

func (m *Manager) snapshots() (snapshotStore, error) {
	data, ok := m.Store.(snapshotStore)
	if !ok {
		return nil, errors.New("durable snapshot storage unavailable")
	}
	return data, nil
}
func snapshotOwnership(r core.InfrastructureSnapshotReview) map[string]string {
	return map[string]string{"dispatch.provider-registration": r.ProviderID, "dispatch.project": r.ProjectID, "dispatch.server": r.ServerID, "dispatch.snapshot": r.SnapshotID, "dispatch.request": r.ID}
}
func ownedSnapshot(r core.InfrastructureSnapshotReview, item provider.Snapshot) error {
	if provider.ValidateSnapshot(item) != nil || item.Name != r.Name {
		return errors.New("snapshot identity does not match review")
	}
	for k, v := range snapshotOwnership(r) {
		if item.Labels[k] != v {
			return errors.New("snapshot ownership does not match review")
		}
	}
	return nil
}
func (m *Manager) ListSnapshots(ctx context.Context, project string) ([]core.InfrastructureSnapshot, error) {
	data, err := m.snapshots()
	if err != nil {
		return nil, err
	}
	items, err := data.ListInfrastructureSnapshots(ctx, project)
	if err != nil {
		return nil, err
	}
	out := []core.InfrastructureSnapshot{}
	for _, item := range items {
		if m.authorize(ctx, item.ProjectID, item.ProviderID, "infrastructure.inspect") == nil {
			out = append(out, item)
		}
	}
	return out, nil
}
func (m *Manager) ReviewSnapshot(ctx context.Context, serverID string, in SnapshotInput) (core.InfrastructureSnapshotReview, error) {
	var result core.InfrastructureSnapshotReview
	data, err := m.snapshots()
	if err != nil {
		return result, err
	}
	source, err := data.GetManagedServer(ctx, serverID)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, source.ProjectID, source.ProviderID, "infrastructure.snapshot"); err != nil {
		return result, err
	}
	if source.AllocationState != "allocated" || in.Name != strings.TrimSpace(in.Name) || len(in.Name) == 0 || len(in.Name) > 80 {
		return result, store.ErrInfrastructureChanged
	}
	now := m.now()
	if in.RetainUntil.IsZero() {
		in.RetainUntil = now.Add(7 * 24 * time.Hour)
	}
	if in.RetainUntil.Before(now) || in.RetainUntil.After(now.Add(365*24*time.Hour)) {
		return result, errors.New("retention must end within the next year")
	}
	client, p, err := m.Adapter(ctx, source.ProviderID, provider.CapabilitySnapshotCreate, "")
	if err != nil {
		return result, err
	}
	var manifest provider.Manifest
	if json.Unmarshal(p.Manifest, &manifest) != nil || manifest.Snapshots == nil || !slices.Contains(p.Capabilities, provider.CapabilitySnapshotInspect) || !slices.Contains(manifest.Snapshots.DiskSets, in.DiskSet) || !slices.Contains(manifest.Snapshots.Consistency, in.Consistency) || !slices.Contains(manifest.Snapshots.Encryption, in.Encryption.Mode) {
		return result, errors.New("provider does not support the selected snapshot policy")
	}
	// Application consistency requires a quiescing protocol; this controller does
	// not yet coordinate it, even if an adapter advertises that future capability.
	if in.Consistency != provider.ConsistencyCrash || in.Encryption.Mode != "provider-managed" || in.Encryption.KeyRef != "" {
		return result, errors.New("only crash-consistent provider-encrypted capture is supported")
	}
	sourceReview, err := data.GetInfrastructureReview(ctx, source.ReviewID)
	if err != nil {
		return result, err
	}
	resource, err := client.Server(ctx, source.ResourceID)
	if err != nil {
		return result, err
	}
	if ownedResource(sourceReview, resource) != nil || resource.State != "ready" || !m.safeEvidence(sourceReview, resource) {
		return result, errors.New("source ownership or disk evidence is unavailable")
	}
	result = core.InfrastructureSnapshotReview{ID: ulid.Make().String(), SnapshotID: ulid.Make().String(), ServerID: source.ID, ProjectID: source.ProjectID, ProviderID: source.ProviderID, ProviderRevision: p.Revision, ManifestDigest: p.ManifestDigest, Name: in.Name, Action: "snapshot.create", State: "open", RetainUntil: in.RetainUntil, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	request := provider.CreateSnapshotRequest{Name: in.Name, SourceServerID: source.ResourceID, DiskSet: in.DiskSet, Consistency: in.Consistency, Encryption: in.Encryption, Labels: snapshotOwnership(result)}
	for _, disk := range resource.Disks {
		if in.DiskSet == "all" || in.DiskSet == "boot" && disk.Role == "boot" {
			if !provider.ValidID(disk.ID) || disk.SizeBytes <= 0 || disk.ContentDigest == "" {
				return result, errors.New("source disk evidence incomplete")
			}
			request.DiskIDs = append(request.DiskIDs, disk.ID)
		}
	}
	if len(request.DiskIDs) == 0 {
		return result, errors.New("no supported source disks")
	}
	// Show exact disk identities and selected consistency alongside retention.
	result.Input, _ = json.Marshal(struct {
		Request     provider.CreateSnapshotRequest `json:"request"`
		RetainUntil time.Time                      `json:"retainUntil"`
	}{request, in.RetainUntil})
	raw, _ := json.Marshal(request)
	result.EncryptedRequest, err = m.Vault.Encrypt("snapshot-review:"+result.ID, raw)
	if err != nil {
		return result, err
	}
	result.Digest = digest(result.EncryptedRequest)
	return result, data.CreateInfrastructureSnapshotReview(ctx, result)
}
func (m *Manager) ReviewSnapshotDelete(ctx context.Context, id string) (core.InfrastructureSnapshotReview, error) {
	var result core.InfrastructureSnapshotReview
	data, err := m.snapshots()
	if err != nil {
		return result, err
	}
	snapshot, err := data.GetInfrastructureSnapshot(ctx, id)
	if err != nil {
		return result, err
	}
	if err = m.authorize(ctx, snapshot.ProjectID, snapshot.ProviderID, "infrastructure.delete"); err != nil {
		return result, err
	}
	if snapshot.RetainUntil.After(m.now()) {
		return result, store.ErrSnapshotProtected
	}
	if !slices.Contains([]string{"ready", "corrupt", "failed"}, snapshot.State) || snapshot.ResourceID == "" {
		return result, store.ErrInfrastructureChanged
	}
	_, p, err := m.Adapter(ctx, snapshot.ProviderID, provider.CapabilitySnapshotDelete, "")
	if err != nil {
		return result, err
	}
	now := m.now()
	result = core.InfrastructureSnapshotReview{ID: ulid.Make().String(), SnapshotID: snapshot.ID, ServerID: snapshot.ServerID, ProjectID: snapshot.ProjectID, ProviderID: snapshot.ProviderID, ProviderRevision: p.Revision, ManifestDigest: p.ManifestDigest, Name: snapshot.Name, Action: "snapshot.delete", State: "open", RetainUntil: snapshot.RetainUntil, CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	result.Input, _ = json.Marshal(snapshot)
	result.EncryptedRequest, err = m.Vault.Encrypt("snapshot-review:"+result.ID, result.Input)
	if err != nil {
		return result, err
	}
	result.Digest = digest(result.EncryptedRequest)
	return result, data.CreateInfrastructureSnapshotReview(ctx, result)
}
func (m *Manager) AcceptSnapshot(ctx context.Context, actor string, in Acceptance) (AcceptedSnapshot, error) {
	var result AcceptedSnapshot
	data, err := m.snapshots()
	if err != nil {
		return result, err
	}
	review, err := data.GetInfrastructureSnapshotReview(ctx, in.ReviewID)
	if err != nil {
		return result, err
	}
	permission := "infrastructure.snapshot"
	if review.Action == "snapshot.delete" {
		permission = "infrastructure.delete"
	}
	if err = m.authorize(ctx, review.ProjectID, review.ProviderID, permission); err != nil {
		return result, err
	}
	if in.ConfirmName != review.Name || in.Digest != review.Digest {
		return result, store.ErrInfrastructureChanged
	}
	var opID string
	if claim, ok := core.MutationAcceptanceFromContext(ctx); ok {
		opID = claim.OperationID
	} else {
		opID, err = operationID(review.ProviderID, actor, in.RequestKey, review.Action)
		if err != nil {
			return result, err
		}
	}
	result.Snapshot, result.Operation, err = data.AcceptInfrastructureSnapshotReview(ctx, review.ID, review.Digest, opID, actor, m.now(), m.Admission)
	if err == nil {
		core.RecordAcceptedOperation(ctx, result.Operation.ID)
	}
	return result, err
}
func (m *Manager) snapshotRequest(review core.InfrastructureSnapshotReview) (provider.CreateSnapshotRequest, error) {
	var result provider.CreateSnapshotRequest
	raw, err := m.Vault.Decrypt("snapshot-review:"+review.ID, review.EncryptedRequest)
	if err != nil {
		return result, err
	}
	if digest(review.EncryptedRequest) != review.Digest || json.Unmarshal(raw, &result) != nil {
		return result, errors.New("saved snapshot review unavailable")
	}
	return result, nil
}

func (m *Manager) ResolveSnapshot(ctx context.Context, id string, in Adoption) (core.InfrastructureSnapshot, error) {
	data, err := m.snapshots()
	if err != nil {
		return core.InfrastructureSnapshot{}, err
	}
	snapshot, err := data.GetInfrastructureSnapshot(ctx, id)
	if err != nil {
		return snapshot, err
	}
	if snapshot.State != "unknown" || snapshot.Name != in.ConfirmName || snapshot.Revision != in.Revision || !provider.ValidID(in.ResourceID) {
		return snapshot, store.ErrInfrastructureChanged
	}
	if err = m.authorize(ctx, snapshot.ProjectID, snapshot.ProviderID, "infrastructure.snapshot"); err != nil {
		return snapshot, err
	}
	operations, err := data.ListInfrastructureOperations(ctx, snapshot.ServerID)
	if err != nil {
		return snapshot, err
	}
	var operation core.InfrastructureOperation
	for _, op := range operations {
		if op.State != "unknown" {
			continue
		}
		r, e := data.SnapshotReviewForOperation(ctx, op.ID)
		if e == nil && r.SnapshotID == id {
			operation = op
			break
		}
	}
	if operation.ID == "" || operation.LeaseUntil.After(m.now()) || operation.ResourceID != "" && operation.ResourceID != in.ResourceID {
		return snapshot, store.ErrInfrastructureChanged
	}
	if operation.Action == "snapshot.delete" {
		if err = m.authorize(ctx, snapshot.ProjectID, snapshot.ProviderID, "infrastructure.delete"); err != nil {
			return snapshot, err
		}
	}
	client, _, err := m.Adapter(ctx, snapshot.ProviderID, provider.CapabilitySnapshotInspect, "")
	if err != nil {
		return snapshot, err
	}
	item, inspectErr := client.Snapshot(ctx, in.ResourceID)
	if inspectErr != nil {
		// Absence during an unfinished/unknown submission is not evidence that a late
		// provider allocation cannot appear. A stable terminal operation is required.
		var absent *provider.Problem
		if !errors.As(inspectErr, &absent) || absent.Status != 404 || operation.ProviderOperationID == "" {
			return snapshot, errors.New("snapshot absence is not yet authoritative")
		}
		remote, e := client.Operation(ctx, operation.ProviderOperationID)
		if e != nil || !provider.Terminal(remote) || remote.ResourceID != in.ResourceID {
			return snapshot, errors.New("snapshot operation disposition remains uncertain")
		}
		snapshot.State = "deleted"
		snapshot.ResourceID = in.ResourceID
	} else {
		capture, e := data.GetInfrastructureSnapshotReview(ctx, snapshot.ReviewID)
		if e != nil {
			return snapshot, e
		}
		request, e := m.snapshotRequest(capture)
		if e != nil {
			return snapshot, e
		}
		source, e := data.GetManagedServer(ctx, snapshot.ServerID)
		if e != nil {
			return snapshot, e
		}
		sourceReview, e := data.GetInfrastructureReview(ctx, source.ReviewID)
		if e != nil {
			return snapshot, e
		}
		if ownedSnapshot(capture, item) != nil || item.SourceServerID != request.SourceServerID || !snapshotDisksMatch(request, item) || item.Consistency != request.Consistency || item.Encryption != request.Encryption || !m.safeEvidence(sourceReview, item) || !slices.Contains([]string{"ready", "corrupt", "failed"}, item.State) {
			return snapshot, errors.New("snapshot identity or captured disk evidence remains unverified")
		}
		snapshot.State = item.State
		snapshot.ResourceID = item.ID
		item.Labels = snapshotOwnership(capture)
		snapshot.Evidence, _ = json.Marshal(item)
	}
	if err = data.ResolveInfrastructureSnapshot(ctx, snapshot, operation.ID, m.now()); err != nil {
		return snapshot, err
	}
	return data.GetInfrastructureSnapshot(ctx, id)
}

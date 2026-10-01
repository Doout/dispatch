package provision

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
)

func (m *Manager) restoreSnapshot(ctx context.Context, in CreateInput) (*provider.Snapshot, error) {
	if in.SourceSnapshotID == "" {
		return nil, nil
	}
	if err := m.authorize(ctx, in.ProjectID, in.ProviderID, "infrastructure.restore"); err != nil {
		return nil, err
	}
	if in.Bootstrap == nil {
		return nil, errors.New("isolated restore requires a fresh pinned cloud-init bootstrap")
	}
	data, err := m.snapshots()
	if err != nil {
		return nil, err
	}
	saved, err := data.GetInfrastructureSnapshot(ctx, in.SourceSnapshotID)
	if err != nil {
		return nil, err
	}
	if saved.ProjectID != in.ProjectID || saved.ProviderID != in.ProviderID || saved.State != "ready" {
		return nil, store.ErrInfrastructureChanged
	}
	client, p, err := m.Adapter(ctx, in.ProviderID, provider.CapabilityRestore, "")
	if err != nil {
		return nil, err
	}
	var manifest provider.Manifest
	if json.Unmarshal(p.Manifest, &manifest) != nil || manifest.Snapshots == nil || !manifest.Snapshots.Restore.SafeClone() || !slices.Contains(p.Capabilities, provider.CapabilitySnapshotInspect) {
		return nil, errors.New("provider cannot guarantee preboot sanitation and isolated restore")
	}
	var expected provider.Snapshot
	if json.Unmarshal(saved.Evidence, &expected) != nil || provider.ValidateSnapshot(expected) != nil {
		return nil, errors.New("saved snapshot evidence unavailable")
	}
	current, err := client.Snapshot(ctx, saved.ResourceID)
	if err != nil {
		return nil, err
	}
	if current.State != "ready" || provider.SnapshotDigest(current) != provider.SnapshotDigest(expected) || current.Image != in.Image {
		return nil, errors.New("snapshot content, availability or image compatibility changed")
	}
	return &expected, nil
}
func (m *Manager) reviewedCloneSnapshot(r core.InfrastructureReview) (provider.Snapshot, error) {
	var value struct {
		Snapshot provider.Snapshot `json:"_dispatchRestoreSnapshot"`
	}
	raw, err := m.Vault.Decrypt("infrastructure-review:"+r.ID, r.EncryptedRequest)
	if err != nil {
		return value.Snapshot, err
	}
	if r.SourceSnapshotID == "" || digest(r.EncryptedRequest) != r.Digest || json.Unmarshal(raw, &value) != nil || provider.ValidateSnapshot(value.Snapshot) != nil {
		return value.Snapshot, errors.New("saved isolated restore review is invalid")
	}
	return value.Snapshot, nil
}
func (m *Manager) verifyReviewedClone(r core.InfrastructureReview, resource provider.Server) error {
	if r.SourceSnapshotID == "" {
		return nil
	}
	snapshot, err := m.reviewedCloneSnapshot(r)
	if err != nil {
		return err
	}
	return provider.VerifyCloneEvidence(snapshot, resource)
}

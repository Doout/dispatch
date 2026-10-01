package provider

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"time"
)

const (
	CapabilitySnapshotCreate  = "snapshot.create"
	CapabilitySnapshotInspect = "snapshot.inspect"
	CapabilitySnapshotDelete  = "snapshot.delete"
	CapabilityRestore         = "server.restore"
	ConsistencyCrash          = "crash-consistent"
	ConsistencyApplication    = "application-consistent"
	IsolationQuarantine       = "quarantine"
)

// SnapshotProvider is optional. Providers without these capabilities continue
// implementing the original server contract unchanged.
type SnapshotProvider interface {
	CreateSnapshot(context.Context, string, CreateSnapshotRequest) (Operation, error)
	Snapshots(context.Context, string) ([]Snapshot, error)
	Snapshot(context.Context, string) (Snapshot, error)
	DeleteSnapshot(context.Context, string, string) (Operation, error)
	RestoreServer(context.Context, string, string, RestoreServerRequest) (Operation, error)
}
type SnapshotCapabilities struct {
	DiskSets    []string            `json:"diskSets"`
	Consistency []string            `json:"consistency"`
	Encryption  []string            `json:"encryption"`
	Restore     RestoreCapabilities `json:"restore"`
}
type RestoreCapabilities struct {
	PrebootSanitization    bool     `json:"prebootSanitization"`
	NetworkQuarantine      bool     `json:"networkQuarantine"`
	ResetAgentIdentity     bool     `json:"resetAgentIdentity"`
	ResetMachineIdentity   bool     `json:"resetMachineIdentity"`
	ResetSSHIdentity       bool     `json:"resetSSHIdentity"`
	ClearRuntimeJournal    bool     `json:"clearRuntimeJournal"`
	DisableCopiedWorkloads bool     `json:"disableCopiedWorkloads"`
	IndependentDisks       bool     `json:"independentDisks"`
	Checks                 []string `json:"checks"`
}

func (c RestoreCapabilities) SafeClone() bool {
	return c.PrebootSanitization && c.NetworkQuarantine && c.ResetAgentIdentity && c.ResetMachineIdentity && c.ResetSSHIdentity && c.ClearRuntimeJournal && c.DisableCopiedWorkloads && c.IndependentDisks
}

type Disk struct {
	ID            string `json:"id"`
	Role          string `json:"role"`
	SizeBytes     int64  `json:"sizeBytes"`
	Encrypted     bool   `json:"encrypted"`
	SourceDiskID  string `json:"sourceDiskId,omitempty"`
	ContentDigest string `json:"contentDigest,omitempty"`
}
type SnapshotEncryption struct {
	Mode   string `json:"mode"`
	KeyRef string `json:"keyRef,omitempty"`
}
type CreateSnapshotRequest struct {
	Name           string             `json:"name"`
	SourceServerID string             `json:"sourceServerId"`
	DiskSet        string             `json:"diskSet"`
	DiskIDs        []string           `json:"diskIds"`
	Consistency    string             `json:"consistency"`
	Encryption     SnapshotEncryption `json:"encryption"`
	Labels         map[string]string  `json:"labels"`
}
type Snapshot struct {
	ID                    string             `json:"id"`
	Name                  string             `json:"name"`
	SourceServerID        string             `json:"sourceServerId"`
	Disks                 []Disk             `json:"disks"`
	State                 string             `json:"state"`
	Consistency           string             `json:"consistency"`
	Encryption            SnapshotEncryption `json:"encryption"`
	CreatedAt             time.Time          `json:"createdAt"`
	Image                 string             `json:"image"`
	SourceMachineIdentity string             `json:"sourceMachineIdentity"`
	SourceSSHIdentity     string             `json:"sourceSSHIdentity"`
	Labels                map[string]string  `json:"labels"`
}
type RestorePolicy struct {
	Isolation               string `json:"isolation"`
	ResetAgentIdentity      bool   `json:"resetAgentIdentity"`
	ResetMachineIdentity    bool   `json:"resetMachineIdentity"`
	ResetSSHIdentity        bool   `json:"resetSSHIdentity"`
	ClearRuntimeJournal     bool   `json:"clearRuntimeJournal"`
	DisableCopiedWorkloads  bool   `json:"disableCopiedWorkloads"`
	AllowProductionBindings bool   `json:"allowProductionBindings"`
}

func IsolatedRestorePolicy() RestorePolicy {
	return RestorePolicy{Isolation: IsolationQuarantine, ResetAgentIdentity: true, ResetMachineIdentity: true, ResetSSHIdentity: true, ClearRuntimeJournal: true, DisableCopiedWorkloads: true}
}
func (p RestorePolicy) SafeClone() bool {
	return p.Isolation == IsolationQuarantine && p.ResetAgentIdentity && p.ResetMachineIdentity && p.ResetSSHIdentity && p.ClearRuntimeJournal && p.DisableCopiedWorkloads && !p.AllowProductionBindings
}

type RestoreServerRequest struct {
	Server                 CreateServerRequest `json:"server"`
	Policy                 RestorePolicy       `json:"policy"`
	ExpectedSnapshotDigest string              `json:"expectedSnapshotDigest"`
}
type RestoreEvidence struct {
	SnapshotID                 string   `json:"snapshotId"`
	Isolation                  string   `json:"isolation"`
	SanitizedBeforeBoot        bool     `json:"sanitizedBeforeBoot"`
	AgentIdentityCleared       bool     `json:"agentIdentityCleared"`
	RuntimeJournalCleared      bool     `json:"runtimeJournalCleared"`
	CopiedWorkloadsDisabled    bool     `json:"copiedWorkloadsDisabled"`
	ProductionBindingsDisabled bool     `json:"productionBindingsDisabled"`
	IndependentDisks           bool     `json:"independentDisks"`
	Checks                     []string `json:"checks"`
}

func (e RestoreEvidence) SafeClone() bool {
	return e.Isolation == IsolationQuarantine && e.SanitizedBeforeBoot && e.AgentIdentityCleared && e.RuntimeJournalCleared && e.CopiedWorkloadsDisabled && e.ProductionBindingsDisabled && e.IndependentDisks
}

func ValidateSnapshot(s Snapshot) error {
	if !ValidID(s.ID) || !ValidID(s.SourceServerID) || s.Name == "" || s.CreatedAt.IsZero() || len(s.Disks) == 0 || len(s.Disks) > 128 || len(s.Name) > 256 || !ValidID(s.Image) || !ValidID(s.SourceMachineIdentity) || !ValidID(s.SourceSSHIdentity) {
		return errors.New("snapshot identity or disk evidence is invalid")
	}
	if !slices.Contains([]string{"pending", "ready", "failed", "deleting", "corrupt"}, s.State) {
		return errors.New("snapshot state is unsupported")
	}
	if s.Consistency != ConsistencyCrash && s.Consistency != ConsistencyApplication {
		return errors.New("snapshot consistency evidence is unsupported")
	}
	seen := map[string]bool{}
	for _, d := range s.Disks {
		checksum, checksumErr := hex.DecodeString(d.ContentDigest)
		if !ValidID(d.ID) || seen[d.ID] || d.SizeBytes <= 0 || checksumErr != nil || len(checksum) != 32 || s.Encryption.Mode == "provider-managed" && !d.Encrypted {
			return errors.New("snapshot disk evidence is invalid")
		}
		seen[d.ID] = true
	}
	return nil
}

// VerifyCloneEvidence checks provider evidence, not database/application health.
// Guest boot is a separate enrolled-agent check performed by the controller.
func VerifyCloneEvidence(snapshot Snapshot, clone Server) error {
	if ValidateSnapshot(snapshot) != nil || snapshot.State != "ready" || !ValidID(clone.ID) {
		return errors.New("clone requires valid ready snapshot evidence")
	}
	if clone.ID == snapshot.SourceServerID || !ValidID(clone.MachineIdentity) || !ValidID(clone.SSHIdentity) || snapshot.SourceMachineIdentity == "" || snapshot.SourceSSHIdentity == "" || clone.MachineIdentity == snapshot.SourceMachineIdentity || clone.SSHIdentity == snapshot.SourceSSHIdentity {
		return errors.New("clone did not establish distinct machine and SSH identities")
	}
	if clone.Restore == nil || clone.Restore.SnapshotID != snapshot.ID || !clone.Restore.SafeClone() {
		return errors.New("clone lacks preboot sanitization and quarantine evidence")
	}
	if len(clone.Disks) != len(snapshot.Disks) {
		return errors.New("clone disk set differs from the snapshot")
	}
	expected := map[string]Disk{}
	for _, disk := range snapshot.Disks {
		expected[disk.ID] = disk
	}
	seen := map[string]bool{}
	cloneIDs := map[string]bool{}
	for _, disk := range clone.Disks {
		source, ok := expected[disk.SourceDiskID]
		if !ok || seen[disk.SourceDiskID] || !ValidID(disk.ID) || cloneIDs[disk.ID] || disk.Role != source.Role || disk.ID == source.ID || disk.SizeBytes < source.SizeBytes || disk.ContentDigest != source.ContentDigest || source.Encrypted && !disk.Encrypted {
			return errors.New("clone disks do not match the captured disk evidence")
		}
		seen[disk.SourceDiskID] = true
		cloneIDs[disk.ID] = true
	}
	return nil
}

// Snapshot metadata is optional unless a snapshot capability is advertised.
// An adapter may expose capture without exposing safe isolated restores.
func validateSnapshotCapabilities(m Manifest, advertised map[string]bool) error {
	enabled := advertised[CapabilitySnapshotCreate] || advertised[CapabilitySnapshotInspect] || advertised[CapabilitySnapshotDelete] || advertised[CapabilityRestore]
	if !enabled {
		if m.Snapshots != nil {
			return errors.New("snapshot metadata requires an advertised snapshot capability")
		}
		return nil
	}
	if m.Snapshots == nil || !advertised[CapabilitySnapshotInspect] {
		return errors.New("snapshot capabilities require inspect support and policy metadata")
	}
	bounded := func(items []string, required bool) bool {
		if len(items) > 32 || required && len(items) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, item := range items {
			if !ValidID(item) || seen[item] {
				return false
			}
			seen[item] = true
		}
		return true
	}
	if !bounded(m.Snapshots.DiskSets, true) || !bounded(m.Snapshots.Consistency, true) || !bounded(m.Snapshots.Encryption, true) || !bounded(m.Snapshots.Restore.Checks, false) {
		return errors.New("snapshot capability policy contains absent, invalid or duplicate values")
	}
	for _, consistency := range m.Snapshots.Consistency {
		if consistency != ConsistencyCrash && consistency != ConsistencyApplication {
			return errors.New("snapshot consistency capability is unsupported")
		}
	}
	if advertised[CapabilityRestore] && (!m.Snapshots.Restore.SafeClone() || !advertised[CapabilityCreate] || !advertised[CapabilityInspect] || !advertised[CapabilityDelete]) {
		return errors.New("restore requires preboot identity sanitation, quarantine and server lifecycle capabilities")
	}
	return nil
}

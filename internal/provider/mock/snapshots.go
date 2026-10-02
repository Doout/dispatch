package mock

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/provider"
)

func ensureServerMetadata(server *provider.Server) {
	if server.MachineIdentity == "" {
		server.MachineIdentity = "machine-" + digest(server.ID)[:24]
	}
	if server.SSHIdentity == "" {
		server.SSHIdentity = "ssh-" + digest("ssh:" + server.ID)[:24]
	}
	if len(server.Disks) == 0 {
		for _, role := range []string{"boot", "data"} {
			id := "mock-disk-" + digest(server.ID + ":" + role)[:24]
			server.Disks = append(server.Disks, provider.Disk{ID: id, Role: role, SizeBytes: 1 << 30, Encrypted: true, ContentDigest: digest("content:" + id)})
		}
	}
}
func snapshotUnsupported() error {
	return provider.NewProblem(422, "Snapshots unsupported", "This adapter does not advertise snapshots or isolated restore.")
}
func (m *Mock) CreateSnapshot(ctx context.Context, key string, in provider.CreateSnapshotRequest) (provider.Operation, error) {
	if m.options.DisableSnapshots {
		return provider.Operation{}, snapshotUnsupported()
	}
	if !provider.ValidID(key) || !provider.ValidID(in.SourceServerID) {
		return provider.Operation{}, provider.NewProblem(400, "Snapshot identity invalid", "Use valid source and mutation identities.")
	}
	raw, _ := json.Marshal(in)
	requestDigest := digest("snapshot.create:" + string(raw))
	keyID := digest(key)
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		source, ok := state.Servers[in.SourceServerID]
		if !ok || source.State != "ready" {
			return provider.Operation{}, provider.NewProblem(409, "Snapshot source unavailable", "Inspect a ready source machine before capturing it.")
		}
		if strings.TrimSpace(in.Name) == "" || len(in.Name) > 80 || in.Consistency != provider.ConsistencyCrash || !slices.Contains([]string{"boot", "all"}, in.DiskSet) || in.Encryption.Mode != "provider-managed" || in.Encryption.KeyRef != "" {
			return provider.Operation{}, provider.NewProblem(422, "Snapshot policy unsupported", "The mock supports boot/all disks, crash consistency and provider-managed encryption.")
		}
		expected := []provider.Disk{}
		for _, disk := range source.Disks {
			if in.DiskSet == "all" || disk.Role == "boot" {
				expected = append(expected, disk)
			}
		}
		if len(expected) != len(in.DiskIDs) {
			return provider.Operation{}, provider.NewProblem(422, "Snapshot disk set changed", "Review the current source disk identities.")
		}
		seen := map[string]bool{}
		for _, id := range in.DiskIDs {
			if seen[id] || !slices.ContainsFunc(expected, func(d provider.Disk) bool { return d.ID == id }) {
				return provider.Operation{}, provider.NewProblem(422, "Snapshot disk identity invalid", "Choose each disk from the source once.")
			}
			seen[id] = true
		}
		labels := map[string]string{}
		for k, v := range in.Labels {
			if len(k) > 128 || len(v) > 128 {
				return provider.Operation{}, provider.NewProblem(422, "Invalid ownership label", "Keep labels within 128 bytes.")
			}
			labels[k] = v
		}
		id := "mock-snapshot-" + keyID[:24]
		op := provider.Operation{ID: "mock-op-" + keyID[:24], State: provider.StatePending, ResourceID: id}
		state.Snapshots[id] = provider.Snapshot{ID: id, Name: in.Name, SourceServerID: source.ID, Disks: expected, State: "pending", Consistency: in.Consistency, Encryption: in.Encryption, CreatedAt: time.Now().UTC(), Image: "mock-linux", SourceMachineIdentity: source.MachineIdentity, SourceSSHIdentity: source.SSHIdentity, Labels: labels}
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Kind: "snapshot", Fail: m.options.FailSnapshot}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}
func (m *Mock) Snapshots(ctx context.Context, source string) ([]provider.Snapshot, error) {
	if m.options.DisableSnapshots {
		return nil, snapshotUnsupported()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items := []provider.Snapshot{}
	for _, snapshot := range m.state.Snapshots {
		if snapshot.SourceServerID == source {
			items = append(items, copySnapshot(snapshot))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}
func copySnapshot(snapshot provider.Snapshot) provider.Snapshot {
	snapshot.Disks = append([]provider.Disk(nil), snapshot.Disks...)
	labels := map[string]string{}
	for k, v := range snapshot.Labels {
		labels[k] = v
	}
	snapshot.Labels = labels
	return snapshot
}
func (m *Mock) Snapshot(ctx context.Context, id string) (provider.Snapshot, error) {
	if m.options.DisableSnapshots {
		return provider.Snapshot{}, snapshotUnsupported()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return provider.Snapshot{}, err
	}
	item, ok := m.state.Snapshots[id]
	if !ok {
		return item, provider.NewProblem(404, "Snapshot not found", "The snapshot is absent.")
	}
	return copySnapshot(item), nil
}
func (m *Mock) DeleteSnapshot(ctx context.Context, key, id string) (provider.Operation, error) {
	if m.options.DisableSnapshots {
		return provider.Operation{}, snapshotUnsupported()
	}
	if !provider.ValidID(key) || !provider.ValidID(id) {
		return provider.Operation{}, provider.NewProblem(400, "Snapshot identity invalid", "Use valid resource and mutation identities.")
	}
	keyID, requestDigest := digest(key), digest("snapshot.delete:"+id)
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		snapshot, exists := state.Snapshots[id]
		if exists && (snapshot.State == "pending" || snapshot.State == "deleting") {
			return provider.Operation{}, provider.NewProblem(409, "Snapshot operation active", "Finish the active snapshot operation before deleting.")
		}
		op := provider.Operation{ID: "mock-op-" + keyID[:24], State: provider.StatePending, ResourceID: id}
		if !exists {
			op.State = provider.StateSucceeded
		} else {
			snapshot.State = "deleting"
			state.Snapshots[id] = snapshot
		}
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Kind: "snapshot", Delete: true, Fail: m.options.FailDelete}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}
func (m *Mock) RestoreServer(ctx context.Context, key, id string, in provider.RestoreServerRequest) (provider.Operation, error) {
	if m.options.DisableSnapshots {
		return provider.Operation{}, snapshotUnsupported()
	}
	if !provider.ValidID(key) || !provider.ValidID(id) {
		return provider.Operation{}, provider.NewProblem(400, "Restore identity invalid", "Use valid snapshot and mutation identities.")
	}
	raw, _ := json.Marshal(in)
	keyID, requestDigest := digest(key), digest("snapshot.restore:"+id+":"+string(raw))
	return m.change(ctx, func(state *savedState) (provider.Operation, error) {
		if prior, ok := state.Keys[keyID]; ok {
			if prior.Digest != requestDigest {
				return provider.Operation{}, conflict()
			}
			return state.Operations[prior.OperationID].Operation, nil
		}
		snapshot, ok := state.Snapshots[id]
		if !ok {
			return provider.Operation{}, provider.NewProblem(404, "Snapshot not found", "The snapshot is absent.")
		}
		if snapshot.State != "ready" || in.ExpectedSnapshotDigest != provider.SnapshotDigest(snapshot) {
			return provider.Operation{}, provider.NewProblem(409, "Snapshot changed or inaccessible", "Inspect the current ready snapshot and review its restore again.")
		}
		input := in.Server
		if !in.Policy.SafeClone() || input.Network != "mock-isolated" || input.Image != snapshot.Image || input.Region != "mock-region" || input.Size != "mock-small" || input.SSHKey == "" || strings.TrimSpace(input.Name) == "" || len(input.Name) > 80 {
			return provider.Operation{}, provider.NewProblem(422, "Isolated restore policy required", "Use an isolated network, matching image and fresh identity/workload policy.")
		}
		if err := m.Validate(ctx, input.ProviderConfig); err != nil {
			return provider.Operation{}, err
		}
		labels := map[string]string{}
		for k, v := range input.Labels {
			if len(k) > 128 || len(v) > 128 {
				return provider.Operation{}, provider.NewProblem(422, "Invalid ownership label", "Keep labels within 128 bytes.")
			}
			labels[k] = v
		}
		serverID := "mock-server-" + keyID[:24]
		server := provider.Server{ID: serverID, Name: input.Name, Address: "192.0.2.11", State: "provisioning", Labels: labels, MachineIdentity: "machine-" + digest(serverID)[:24], SSHIdentity: "ssh-" + digest("ssh:" + serverID)[:24]}
		for _, disk := range snapshot.Disks {
			server.Disks = append(server.Disks, provider.Disk{ID: "mock-disk-" + digest(serverID + ":" + disk.ID)[:24], Role: disk.Role, SizeBytes: disk.SizeBytes, Encrypted: disk.Encrypted, SourceDiskID: disk.ID, ContentDigest: disk.ContentDigest})
		}
		server.Restore = &provider.RestoreEvidence{SnapshotID: id, Isolation: provider.IsolationQuarantine, SanitizedBeforeBoot: true, AgentIdentityCleared: true, RuntimeJournalCleared: true, CopiedWorkloadsDisabled: true, ProductionBindingsDisabled: true, IndependentDisks: true, Checks: []string{"provider-disk-map", "provider-content-digest"}}
		if m.options.UnsafeRestore {
			server.MachineIdentity = snapshot.SourceMachineIdentity
			server.SSHIdentity = snapshot.SourceSSHIdentity
			server.Restore.AgentIdentityCleared = false
			server.Restore.RuntimeJournalCleared = false
		}
		state.Servers[server.ID] = server
		op := provider.Operation{ID: "mock-op-" + keyID[:24], State: provider.StatePending, ResourceID: server.ID}
		state.Operations[op.ID] = savedOperation{Operation: op, Remaining: m.options.Polls, Fail: m.options.FailRestore}
		state.Keys[keyID] = savedKey{Digest: requestDigest, OperationID: op.ID}
		return op, nil
	})
}

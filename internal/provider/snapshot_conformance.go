package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"time"
)

// RunSnapshotConformance allocates a source machine, a retained snapshot and an
// isolated clone. Callers must explicitly authorize these additional mutations.
// It validates provider evidence; it does not prove a real guest booted or that
// an application-consistent backup can be recovered.
func RunSnapshotConformance(ctx context.Context, adapter Provider, options ConformanceOptions) (report ConformanceReport, resultErr error) {
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	if options.Timeout <= 0 {
		options.Timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	report = ConformanceReport{APIVersion: APIVersion, Checks: []ConformanceCheck{}}
	check := func(name string, run func() error) bool {
		err := run()
		item := ConformanceCheck{Name: name, Passed: err == nil}
		if err != nil {
			item.Message = err.Error()
			resultErr = fmt.Errorf("snapshot conformance failed: %s", name)
		}
		report.Checks = append(report.Checks, item)
		return err == nil
	}
	snapshots, ok := adapter.(SnapshotProvider)
	if !ok {
		return report, errors.New("adapter has no snapshot implementation")
	}
	var restoreNetwork string
	if !check("isolated snapshot capabilities", func() error {
		m, err := adapter.Manifest(ctx)
		if err != nil || ValidateManifest(m) != nil || m.Snapshots == nil || !m.Snapshots.Restore.SafeClone() {
			return errors.New("snapshot manifest lacks safe isolated restore capabilities")
		}
		for _, capability := range []string{CapabilitySnapshotCreate, CapabilitySnapshotInspect, CapabilitySnapshotDelete, CapabilityRestore} {
			if !slices.Contains(m.Capabilities, capability) {
				return errors.New("snapshot lifecycle capability set is incomplete")
			}
		}
		if !slices.Contains(m.Snapshots.DiskSets, "all") || !slices.Contains(m.Snapshots.Consistency, ConsistencyCrash) || !slices.Contains(m.Snapshots.Encryption, "provider-managed") {
			return errors.New("conformance requires all-disk crash-consistent capture with provider-managed encryption")
		}
		networks, err := adapter.Options(ctx, OptionRequest{Kind: "restore-networks", Config: options.Request.ProviderConfig})
		if err != nil {
			return errors.New("restore network discovery failed")
		}
		for _, network := range networks {
			if quarantine, _ := network.Metadata["quarantine"].(bool); quarantine && ValidID(network.ID) {
				restoreNetwork = network.ID
				break
			}
		}
		if restoreNetwork == "" {
			return errors.New("no quarantined restore network was advertised")
		}
		return nil
	}) {
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return report, errors.New("cannot generate snapshot test identity")
	}
	prefix := "snapshot-conformance-" + hex.EncodeToString(random)
	var sourceOp, captureOp, cloneOp Operation
	var source Server
	var snapshot Snapshot
	cleaned := false
	mutationOutcomeUnknown := false
	defer func() {
		if cleaned {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.Timeout)
		defer cancel()
		uncertain := mutationOutcomeUnknown
		// Inspect accepted operations before cleanup. Never invent another create key.
		for _, op := range []*Operation{&sourceOp, &captureOp, &cloneOp} {
			if op.ID != "" && !Terminal(*op) {
				observed, err := waitOperation(cleanupCtx, adapter, *op, options.PollInterval)
				if err != nil {
					uncertain = true
				} else {
					*op = observed
				}
			}
		}
		for i, op := range []Operation{cloneOp, sourceOp} {
			if op.ResourceID == "" {
				continue
			}
			deletion, err := adapter.DeleteServer(cleanupCtx, fmt.Sprintf("%s-cleanup-server-%d", prefix, i), op.ResourceID)
			if err == nil {
				deletion, err = waitOperation(cleanupCtx, adapter, deletion, options.PollInterval)
			}
			if err != nil || deletion.State != StateSucceeded {
				uncertain = true
			}
		}
		if captureOp.ResourceID != "" {
			deletion, err := snapshots.DeleteSnapshot(cleanupCtx, prefix+"-cleanup-snapshot", captureOp.ResourceID)
			if err == nil {
				deletion, err = waitOperation(cleanupCtx, adapter, deletion, options.PollInterval)
			}
			if err != nil || deletion.State != StateSucceeded {
				uncertain = true
			}
		}
		item := ConformanceCheck{Name: "snapshot failure cleanup", Passed: !uncertain}
		if uncertain {
			item.Message = "cleanup needs inspection; retain the isolated test account and operation history"
			if resultErr == nil {
				resultErr = errors.New(item.Message)
			}
		}
		report.Checks = append(report.Checks, item)
	}()
	if !check("snapshot source allocation", func() error {
		request := options.Request
		request.Name = prefix
		var err error
		mutationOutcomeUnknown = true
		sourceOp, err = adapter.CreateServer(ctx, prefix+"-source", request)
		if err != nil {
			return errors.New("source allocation was not accepted")
		}
		mutationOutcomeUnknown = false
		sourceOp, err = waitOperation(ctx, adapter, sourceOp, options.PollInterval)
		if err != nil || sourceOp.State != StateSucceeded {
			return errors.New("source allocation did not succeed")
		}
		source, err = adapter.Server(ctx, sourceOp.ResourceID)
		if err != nil || source.State != "ready" || len(source.Disks) == 0 {
			return errors.New("ready source disk evidence is missing")
		}
		return nil
	}) {
		return
	}
	if !check("capture idempotency and retained disk evidence", func() error {
		in := CreateSnapshotRequest{Name: prefix, SourceServerID: source.ID, DiskSet: "all", Consistency: ConsistencyCrash, Encryption: SnapshotEncryption{Mode: "provider-managed"}, Labels: map[string]string{"dispatch.conformance": prefix}}
		for _, disk := range source.Disks {
			in.DiskIDs = append(in.DiskIDs, disk.ID)
		}
		var err error
		mutationOutcomeUnknown = true
		captureOp, err = snapshots.CreateSnapshot(ctx, prefix+"-capture", in)
		if err != nil {
			return errors.New("capture was not accepted")
		}
		mutationOutcomeUnknown = false
		again, err := snapshots.CreateSnapshot(ctx, prefix+"-capture", in)
		if err != nil || again.ID != captureOp.ID || again.ResourceID != captureOp.ResourceID {
			return errors.New("capture replay changed operation or resource identity")
		}
		changed := in
		changed.Name = "changed"
		_, err = snapshots.CreateSnapshot(ctx, prefix+"-capture", changed)
		var problem *Problem
		if !errors.As(err, &problem) || problem.Status != http.StatusConflict {
			return errors.New("changed capture inputs did not conflict")
		}
		captureOp, err = waitOperation(ctx, adapter, captureOp, options.PollInterval)
		if err != nil || captureOp.State != StateSucceeded {
			return errors.New("capture did not succeed")
		}
		snapshot, err = snapshots.Snapshot(ctx, captureOp.ResourceID)
		if err != nil || ValidateSnapshot(snapshot) != nil || snapshot.State != "ready" || snapshot.SourceServerID != source.ID || snapshot.Consistency != ConsistencyCrash || !reflect.DeepEqual(snapshot.Disks, source.Disks) {
			return errors.New("snapshot disk set or consistency differs from reviewed source")
		}
		items, err := snapshots.Snapshots(ctx, source.ID)
		if err != nil || !slices.ContainsFunc(items, func(s Snapshot) bool { return s.ID == snapshot.ID }) {
			return errors.New("captured snapshot is absent from source inventory")
		}
		return nil
	}) {
		return
	}
	if !check("restore replay and preboot isolation evidence", func() error {
		request := options.Request
		request.Name = prefix + "-clone"
		request.Network = restoreNetwork
		request.Image = snapshot.Image
		in := RestoreServerRequest{Server: request, Policy: IsolatedRestorePolicy(), ExpectedSnapshotDigest: SnapshotDigest(snapshot)}
		var err error
		mutationOutcomeUnknown = true
		cloneOp, err = snapshots.RestoreServer(ctx, prefix+"-restore", snapshot.ID, in)
		if err != nil {
			return errors.New("isolated restore was not accepted")
		}
		mutationOutcomeUnknown = false
		again, err := snapshots.RestoreServer(ctx, prefix+"-restore", snapshot.ID, in)
		if err != nil || again.ID != cloneOp.ID || again.ResourceID != cloneOp.ResourceID {
			return errors.New("restore replay changed operation or resource identity")
		}
		cloneOp, err = waitOperation(ctx, adapter, cloneOp, options.PollInterval)
		if err != nil || cloneOp.State != StateSucceeded {
			return errors.New("restore did not succeed")
		}
		clone, err := adapter.Server(ctx, cloneOp.ResourceID)
		if err != nil || clone.State != "ready" {
			return errors.New("clone is not inspectable and ready")
		}
		if err = VerifyCloneEvidence(snapshot, clone); err != nil {
			return err
		}
		current, err := adapter.Server(ctx, source.ID)
		if err != nil || !reflect.DeepEqual(current, source) {
			return errors.New("restore modified the source machine")
		}
		return nil
	}) {
		return
	}
	if !check("source and clone deletion preserve retained snapshot", func() error {
		for i, op := range []Operation{sourceOp, cloneOp} {
			deletion, err := adapter.DeleteServer(ctx, fmt.Sprintf("%s-delete-machine-%d", prefix, i), op.ResourceID)
			if err == nil {
				deletion, err = waitOperation(ctx, adapter, deletion, options.PollInterval)
			}
			if err != nil || deletion.State != StateSucceeded {
				return errors.New("test machine cleanup failed")
			}
			_, err = adapter.Server(ctx, op.ResourceID)
			var problem *Problem
			if !errors.As(err, &problem) || problem.Status != 404 {
				return errors.New("deleted test machine remains inspectable")
			}
			current, err := snapshots.Snapshot(ctx, snapshot.ID)
			if err != nil || SnapshotDigest(current) != SnapshotDigest(snapshot) {
				return errors.New("machine deletion modified retained snapshot")
			}
		}
		return nil
	}) {
		return
	}
	if !check("snapshot deletion idempotency and absence", func() error {
		deletion, err := snapshots.DeleteSnapshot(ctx, prefix+"-delete-snapshot", snapshot.ID)
		if err != nil {
			return errors.New("snapshot deletion was not accepted")
		}
		again, err := snapshots.DeleteSnapshot(ctx, prefix+"-delete-snapshot", snapshot.ID)
		if err != nil || again.ID != deletion.ID || again.ResourceID != deletion.ResourceID {
			return errors.New("snapshot deletion replay changed identity")
		}
		deletion, err = waitOperation(ctx, adapter, deletion, options.PollInterval)
		if err != nil || deletion.State != StateSucceeded {
			return errors.New("snapshot deletion did not succeed")
		}
		_, err = snapshots.Snapshot(ctx, snapshot.ID)
		var problem *Problem
		if !errors.As(err, &problem) || problem.Status != 404 {
			return errors.New("deleted snapshot remains inspectable")
		}
		return nil
	}) {
		return
	}
	cleaned = true
	return
}

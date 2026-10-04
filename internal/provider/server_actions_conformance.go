package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"time"
)

// RunServerActionConformance exercises disposable machines, optional power
// operations and clone promotion. It removes the machines and snapshot it creates.
func RunServerActionConformance(ctx context.Context, adapter Provider, options ConformanceOptions) (report ConformanceReport, resultErr error) {
	if options.Timeout <= 0 {
		options.Timeout = 2 * time.Minute
	}
	if options.PollInterval <= 0 {
		options.PollInterval = 100 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	report = ConformanceReport{APIVersion: APIVersion, Checks: []ConformanceCheck{}}
	check := func(name string, run func() error) bool {
		e := run()
		report.Checks = append(report.Checks, ConformanceCheck{Name: name, Passed: e == nil})
		if e != nil {
			report.Checks[len(report.Checks)-1].Message = e.Error()
			resultErr = errors.New("machine action conformance failed")
		}
		return e == nil
	}
	manifest, e := adapter.Manifest(ctx)
	if e != nil || ValidateManifest(manifest) != nil {
		return report, errors.New("machine action manifest is unavailable")
	}
	powers, ok := adapter.(PowerProvider)
	if !ok {
		return report, errors.New("machine power interface is unavailable")
	}
	for _, capability := range []string{CapabilityPowerStart, CapabilityPowerStop, CapabilityPowerReboot} {
		if !slices.Contains(manifest.Capabilities, capability) {
			return report, errors.New("machine action conformance requires start, stop and reboot")
		}
	}
	random := make([]byte, 12)
	if _, e = rand.Read(random); e != nil {
		return report, e
	}
	prefix := "actions-" + hex.EncodeToString(random)
	var source, clone Server
	var snapshot Snapshot
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.Timeout)
		defer cancel()
		failed := false
		for _, s := range []Server{clone, source} {
			if s.ID == "" {
				continue
			}
			op, e := adapter.DeleteServer(cleanupCtx, prefix+"-delete-"+s.ID, s.ID)
			if e == nil {
				op, e = waitOperation(cleanupCtx, adapter, op, options.PollInterval)
			}
			if e != nil || op.State != StateSucceeded {
				failed = true
			}
		}
		if snapshot.ID != "" {
			p, ok := adapter.(SnapshotProvider)
			if !ok {
				failed = true
			} else {
				op, e := p.DeleteSnapshot(cleanupCtx, prefix+"-snapshot-delete", snapshot.ID)
				if e == nil {
					op, e = waitOperation(cleanupCtx, adapter, op, options.PollInterval)
				}
				if e != nil || op.State != StateSucceeded {
					failed = true
				}
			}
		}
		report.Checks = append(report.Checks, ConformanceCheck{Name: "machine action cleanup", Passed: !failed})
		if failed {
			resultErr = errors.New("machine action cleanup requires inspection")
		}
	}()
	if !check("machine action source allocation", func() error {
		op, e := adapter.CreateServer(ctx, prefix+"-create", options.Request)
		if e != nil {
			return errors.New("source allocation unavailable")
		}
		source.ID = op.ResourceID
		op, e = waitOperation(ctx, adapter, op, options.PollInterval)
		if e != nil || op.State != StateSucceeded {
			return errors.New("source allocation unsuccessful")
		}
		source, e = adapter.Server(ctx, op.ResourceID)
		return e
	}) {
		return
	}
	for _, action := range []string{"stop", "start", "reboot"} {
		if !check("machine "+action+" replay and evidence", func() error {
			current, e := adapter.Server(ctx, source.ID)
			if e != nil {
				return e
			}
			in := PowerServerRequest{Action: action, ExpectedIdentity: ServerIdentityDigest(current)}
			key := prefix + "-" + action
			op, e := powers.PowerServer(ctx, key, source.ID, in)
			if e != nil {
				return errors.New("machine power action unavailable")
			}
			again, e := powers.PowerServer(ctx, key, source.ID, in)
			if e != nil || op.ID != again.ID || op.ResourceID != again.ResourceID {
				return errors.New("machine power replay changed identity")
			}
			changed := in
			changed.Action = "start"
			if action == "start" {
				changed.Action = "stop"
			}
			_, e = powers.PowerServer(ctx, key, source.ID, changed)
			var problem *Problem
			if !errors.As(e, &problem) || problem.Status != http.StatusConflict {
				return errors.New("machine power key accepted changed input")
			}
			op, e = waitOperation(ctx, adapter, op, options.PollInterval)
			if e != nil || op.State != StateSucceeded {
				return errors.New("machine power action did not complete")
			}
			current, e = adapter.Server(ctx, source.ID)
			if e != nil {
				return e
			}
			return VerifyPowerEvidence(current, op.ID, in)
		}) {
			return
		}
	}
	if slices.Contains(manifest.Capabilities, CapabilityPromote) {
		snapshots, ok := adapter.(SnapshotProvider)
		if !ok {
			return report, errors.New("promotion requires snapshot implementation")
		}
		promotions, ok := adapter.(PromotionProvider)
		if !ok {
			return report, errors.New("promotion implementation is absent")
		}
		if !check("independent clone capture and restore", func() error {
			source, e = adapter.Server(ctx, source.ID)
			if e != nil {
				return e
			}
			in := CreateSnapshotRequest{Name: prefix, SourceServerID: source.ID, DiskSet: "all", Consistency: ConsistencyCrash, Encryption: SnapshotEncryption{Mode: "provider-managed"}, Labels: map[string]string{"dispatch.conformance": prefix}}
			for _, disk := range source.Disks {
				in.DiskIDs = append(in.DiskIDs, disk.ID)
			}
			capture, e := snapshots.CreateSnapshot(ctx, prefix+"-capture", in)
			if e != nil {
				return e
			}
			snapshot.ID = capture.ResourceID
			capture, e = waitOperation(ctx, adapter, capture, options.PollInterval)
			if e != nil || capture.State != StateSucceeded {
				return errors.New("capture unsuccessful")
			}
			snapshot, e = snapshots.Snapshot(ctx, capture.ResourceID)
			if e != nil {
				return e
			}
			networks, e := adapter.Options(ctx, OptionRequest{Kind: "restore-networks", Config: options.Request.ProviderConfig})
			if e != nil {
				return e
			}
			network := ""
			for _, option := range networks {
				if option.Metadata["quarantine"] == true {
					network = option.ID
					break
				}
			}
			if network == "" {
				return errors.New("quarantine network absent")
			}
			request := options.Request
			request.Network = network
			request.Name = prefix + "-clone"
			restore, e := snapshots.RestoreServer(ctx, prefix+"-restore", snapshot.ID, RestoreServerRequest{Server: request, Policy: IsolatedRestorePolicy(), ExpectedSnapshotDigest: SnapshotDigest(snapshot)})
			if e != nil {
				return e
			}
			clone.ID = restore.ResourceID
			restore, e = waitOperation(ctx, adapter, restore, options.PollInterval)
			if e != nil || restore.State != StateSucceeded {
				return errors.New("clone restore unsuccessful")
			}
			clone, e = adapter.Server(ctx, clone.ID)
			if e != nil {
				return e
			}
			return VerifyCloneEvidence(snapshot, clone)
		}) {
			return
		}
		if !check("clone promotion replay and authoritative release", func() error {
			in := PromoteServerRequest{Network: options.Request.Network, ExpectedIdentity: ServerIdentityDigest(clone)}
			op, e := promotions.PromoteServer(ctx, prefix+"-promote", clone.ID, in)
			if e != nil {
				return errors.New("clone promotion unavailable")
			}
			again, e := promotions.PromoteServer(ctx, prefix+"-promote", clone.ID, in)
			if e != nil || op.ID != again.ID || op.ResourceID != again.ResourceID {
				return errors.New("promotion replay changed identity")
			}
			changed := in
			changed.Network = "changed-network"
			_, e = promotions.PromoteServer(ctx, prefix+"-promote", clone.ID, changed)
			var problem *Problem
			if !errors.As(e, &problem) || problem.Status != 409 {
				return errors.New("promotion key accepted changed network")
			}
			op, e = waitOperation(ctx, adapter, op, options.PollInterval)
			if e != nil || op.State != StateSucceeded {
				return errors.New("promotion unsuccessful")
			}
			current, e := adapter.Server(ctx, clone.ID)
			if e != nil {
				return e
			}
			if e = VerifyPromotionEvidence(current, op.ID, in); e != nil {
				return e
			}
			original, e := adapter.Server(ctx, source.ID)
			if e != nil || ServerIdentityDigest(original) != ServerIdentityDigest(source) {
				return errors.New("promotion changed the source")
			}
			return VerifyCloneEvidence(snapshot, current)
		}) {
			return
		}
	}
	return
}

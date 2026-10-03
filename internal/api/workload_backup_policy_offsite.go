package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func policyOffsiteStoreDigest(item core.BackupObjectStore) string {
	raw, _ := json.Marshal(struct {
		ID, ProjectID, CredentialSecretID, Endpoint, Bucket, Region, Prefix string
		MaxBytes                                                            int64
	}{item.ID, item.ProjectID, item.CredentialSecretID, item.Config.Endpoint, item.Config.Bucket, item.Config.Region, item.Config.Prefix, item.Config.MaxBytes})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Registration is immutable. The frozen digest also detects changed controller records.
func (a *API) getPolicyOffsiteStore(ctx context.Context, p core.WorkloadBackupPolicy) (core.BackupObjectStore, error) {
	stores, err := a.backupObjectStore()
	if err != nil {
		return core.BackupObjectStore{}, err
	}
	item, err := stores.GetBackupObjectStore(ctx, p.OffsiteStoreID)
	if err != nil {
		return item, err
	}
	if item.ProjectID != p.ProjectID || p.OffsiteStoreDigest != "" && p.OffsiteStoreDigest != policyOffsiteStoreDigest(item) {
		return item, store.ErrWorkloadBackupChanged
	}
	if _, err = a.objectStoreCredentials(ctx, item); err != nil {
		return item, err
	}
	return item, nil
}

func (a *API) backupPolicyNotificationConfig(ctx context.Context, p core.WorkloadBackupPolicy) (core.ObservationConfig, error) {
	app, err := a.store.GetApp(ctx, p.NotificationAppID)
	if err != nil || app.ProjectID != p.ProjectID || a.observations == nil {
		return core.ObservationConfig{}, errors.New("the notification application must belong to the policy project")
	}
	config, err := a.observations.Config(ctx, app.ID)
	if err != nil || !config.NotificationsEnabled || !config.WebhookConfigured {
		return config, errors.New("explicitly enable the existing application notification destination first")
	}
	return config, nil
}

func (a *API) acceptBackupPolicyExport(ctx context.Context, p core.WorkloadBackupPolicy, b core.WorkloadBackup) error {
	if err := a.backupPolicyAuthority(ctx, p); err != nil {
		return err
	}
	input, err := a.decryptWorkloadBackup(b.ID, "accepted", b.EncryptedInput)
	if err != nil {
		return err
	}
	access, err := a.grantBackupObjects(ctx, b, p.OffsiteStoreID, true, false)
	if err != nil {
		return err
	}
	op := newBackupOperation(ulid.Make().String(), b, "export")
	op.CapturePolicyID, op.OffsiteStoreID = p.ID, p.OffsiteStoreID
	op.ExecutionServerID, op.ExecutionNodeID, op.ExecutionGeneration = p.ServerID, p.NodeID, p.NodeGeneration
	input.Backup, input.OperationID, input.Action, input.OffsiteAccess = b, op.ID, "export", &access
	input.ExecutionNodeGeneration = p.NodeGeneration
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", input)
	if err != nil {
		return err
	}
	policies, _ := a.backupPolicyStore()
	if err = policies.AcceptWorkloadBackupExport(ctx, p, b, op); err != nil {
		return err
	}
	go a.executeWorkloadBackupOperation(op, false)
	return nil
}

// Each tick can accept one export. The original capture owns its durable operation ID.
func (a *API) scheduleBackupPolicyOffsite(ctx context.Context, p core.WorkloadBackupPolicy, authorized bool) core.WorkloadBackupPolicy {
	if p.OffsiteStoreID == "" {
		return p
	}
	policies, err := a.backupPolicyStore()
	if err != nil {
		return p
	}
	backups, err := a.workloadBackupStore()
	if err != nil {
		return p
	}
	items, err := backups.ListWorkloadBackups(ctx, p.ProjectID)
	if err != nil {
		return p
	}
	operations := map[string][]core.WorkloadBackupOperation{}
	var candidate core.WorkloadBackup
	busy := false
	for _, b := range items {
		if b.CapturePolicyID != p.ID {
			continue
		}
		ops, err := backups.ListWorkloadBackupOperations(ctx, b.ID)
		if err != nil {
			return p
		}
		operations[b.ID] = ops
		failed := false
		for _, op := range ops {
			if op.State == "running" || op.State == "unknown" || op.State == "unresolved" {
				busy = true
			}
			if op.ID == b.ScheduledExportOperationID && op.State == "failed" {
				failed = true
			}
		}
		eligible := b.State == "ready" && b.VerificationState == "verified" && b.CleanupState == "complete" && b.LocalState != "retired" && b.LocalState != "retiring"
		if failed || eligible && !authorized && (b.Offsite == nil || b.Offsite.DeletedAt != nil) {
			if err := policies.MarkWorkloadBackupExportMissed(ctx, p, b); err == nil {
				p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
			}
		}
		if eligible && b.Offsite == nil && b.ScheduledExportOperationID == "" {
			candidate = b
		}
	}
	if authorized && !busy && candidate.ID != "" {
		// A missed-export update may have changed the backup revision.
		candidate, err = backups.GetWorkloadBackup(ctx, candidate.ID)
		if err == nil {
			if err = a.acceptBackupPolicyExport(ctx, p, candidate); err != nil && !errors.Is(err, store.ErrWorkloadBackupChanged) {
				_ = policies.MarkWorkloadBackupExportMissed(ctx, p, candidate)
			}
		}
		if latest, lookup := policies.GetWorkloadBackupPolicy(ctx, p.ID); lookup == nil {
			p = latest
		}
		// Refresh archive and operation evidence after acceptance, never infer success from queueing.
		items, err = backups.ListWorkloadBackups(ctx, p.ProjectID)
		if err != nil {
			return p
		}
		for _, b := range items {
			if b.CapturePolicyID == p.ID {
				operations[b.ID], _ = backups.ListWorkloadBackupOperations(ctx, b.ID)
			}
		}
	}
	before := p
	p = describeBackupPolicyOffsite(p, items, operations)
	if !authorized {
		p.OffsiteState, p.OffsiteMessage = "blocked", "The original actor, enrollment or approved destination no longer authorizes unattended offsite work."
	}
	p.OffsiteFreshness = p.OffsiteFreshnessAt(time.Now().UTC())
	p.RetentionBlockedReason = p.RetentionBlocker()
	event := a.backupPolicyOffsiteEvent(ctx, &p)
	if !reflect.DeepEqual(before, p) {
		if err = policies.UpdateWorkloadBackupPolicyEvent(ctx, p, p.Revision, event); err == nil {
			p.Revision++
		} else {
			p, _ = policies.GetWorkloadBackupPolicy(ctx, p.ID)
		}
	}
	return p
}

func (a *API) backupPolicyOffsiteEvent(ctx context.Context, p *core.WorkloadBackupPolicy) *core.ObservationEvent {
	if p.NotificationAppID == "" {
		return nil
	}
	state := p.OffsiteFreshness
	if p.OffsiteState == "blocked" || p.OffsiteState == "export_failed" || p.OffsiteState == "verification_failed" {
		state = p.OffsiteState
	}
	if p.OffsiteNotificationState == state {
		return nil
	}
	config, err := a.backupPolicyNotificationConfig(ctx, *p)
	if err != nil {
		return nil
	}
	previous := p.OffsiteNotificationState
	p.OffsiteNotificationState = state
	bad := func(s string) bool {
		return s == "stale" || s == "blocked" || s == "export_failed" || s == "verification_failed"
	}
	if !bad(state) && !(state == "fresh" && bad(previous)) {
		return nil
	}
	p.OffsiteNotificationSequence++
	now := time.Now().UTC()
	message := "Scheduled offsite protection changed. " + p.OffsiteMessage
	if state == "stale" {
		message = "The captured data in the last confirmed offsite recovery point is older than the policy freshness threshold."
	}
	if state == "fresh" {
		message = "A fresh captured recovery point passed isolated offsite restore verification."
	}
	e := core.ObservationEvent{ID: fmt.Sprintf("backup-offsite-%s-%s-%s-%d", p.ID, p.LastBackupID, state, p.OffsiteNotificationSequence), AppID: config.AppID, ProjectID: p.ProjectID, ConfigurationRevision: config.Revision, Kind: "backup_offsite", PreviousState: previous, State: state, Message: message, Link: "/api/v1/workload-backup-policies/" + p.ID, CreatedAt: now, Delivery: "pending", NextAttemptAt: &now}
	if config.MutedUntil != nil && config.MutedUntil.After(now) {
		e.Delivery, e.NextAttemptAt = "muted", nil
	}
	return &e
}

func verifiedPolicyOffsite(p core.WorkloadBackupPolicy, b core.WorkloadBackup) bool {
	return b.Offsite != nil && b.Offsite.DeletedAt == nil && b.Offsite.StoreID == p.OffsiteStoreID && !b.Offsite.ConfirmedAt.IsZero() && b.Offsite.VerificationState == "verified" && b.Offsite.VerifiedAt != nil && b.Offsite.ManifestChecksum != "" && b.Checksum != ""
}

// Public inspection reflects current retained copies without accepting jobs or changing revisions.
func (a *API) inspectBackupPolicyOffsite(ctx context.Context, p core.WorkloadBackupPolicy) core.WorkloadBackupPolicy {
	return a.inspectBackupPoliciesOffsite(ctx, []core.WorkloadBackupPolicy{p})[0]
}

type backupPolicyInspectionStore interface {
	ListWorkloadBackups(context.Context, string) ([]core.WorkloadBackup, error)
	ListWorkloadBackupOperations(context.Context, string) ([]core.WorkloadBackupOperation, error)
}

type backupPolicyOffsiteInspection struct {
	policies       map[string]bool
	items          []core.WorkloadBackup
	operations     map[string][]core.WorkloadBackupOperation
	failedPolicies map[string]bool
	available      bool
}

// Callers filter policies for visibility before loading this request's inspection evidence.
func (a *API) inspectBackupPoliciesOffsite(ctx context.Context, policies []core.WorkloadBackupPolicy) []core.WorkloadBackupPolicy {
	projects := map[string]*backupPolicyOffsiteInspection{}
	projectIDs := []string{}
	for _, p := range policies {
		if p.OffsiteStoreID == "" {
			continue
		}
		inspection := projects[p.ProjectID]
		if inspection == nil {
			inspection = &backupPolicyOffsiteInspection{policies: map[string]bool{}, operations: map[string][]core.WorkloadBackupOperation{}, failedPolicies: map[string]bool{}}
			projects[p.ProjectID] = inspection
			projectIDs = append(projectIDs, p.ProjectID)
		}
		inspection.policies[p.ID] = true
	}
	if backups, ok := a.store.(backupPolicyInspectionStore); ok {
		for _, projectID := range projectIDs {
			inspection := projects[projectID]
			items, err := backups.ListWorkloadBackups(ctx, projectID)
			if err != nil {
				continue
			}
			inspection.items, inspection.available = items, true
			for _, b := range items {
				if !inspection.policies[b.CapturePolicyID] || inspection.failedPolicies[b.CapturePolicyID] {
					continue
				}
				if _, loaded := inspection.operations[b.ID]; loaded {
					continue
				}
				operations, err := backups.ListWorkloadBackupOperations(ctx, b.ID)
				if err != nil {
					// A failed read retains this policy's recorded projection, not its siblings'.
					inspection.failedPolicies[b.CapturePolicyID] = true
					continue
				}
				inspection.operations[b.ID] = operations
			}
		}
	}
	for i, p := range policies {
		policies[i] = projectBackupPolicyOffsite(p, projects[p.ProjectID], time.Now().UTC())
	}
	return policies
}

func projectBackupPolicyOffsite(p core.WorkloadBackupPolicy, inspection *backupPolicyOffsiteInspection, now time.Time) core.WorkloadBackupPolicy {
	if p.OffsiteStoreID == "" {
		p.OffsiteFreshness = p.OffsiteFreshnessAt(now)
		return p
	}
	if inspection == nil || !inspection.available || inspection.failedPolicies[p.ID] {
		return p
	}
	oldState, oldMessage := p.OffsiteState, p.OffsiteMessage
	p = describeBackupPolicyOffsite(p, inspection.items, inspection.operations)
	// Inspection does not silently clear a recorded unattended authority blocker.
	if oldState == "blocked" && p.OffsiteState != "blocked" && p.State == "blocked" {
		p.OffsiteState, p.OffsiteMessage = oldState, oldMessage
	}
	p.OffsiteFreshness = p.OffsiteFreshnessAt(now)
	p.RetentionBlockedReason = p.RetentionBlocker()
	return p
}

func describeBackupPolicyOffsite(p core.WorkloadBackupPolicy, items []core.WorkloadBackup, operations map[string][]core.WorkloadBackupOperation) core.WorkloadBackupPolicy {
	p.LastOffsiteBackupID, p.LastOffsiteRecoveryPointAt, p.LastOffsiteVerifiedAt = "", nil, nil
	p.OffsiteState, p.OffsiteMessage = "pending", "The captured recovery point is awaiting offsite export and isolated verification."
	for _, b := range items {
		if b.CapturePolicyID != p.ID {
			continue
		}
		retiring := false
		for _, op := range operations[b.ID] {
			if (op.Action == "retire-local" || op.Action == "delete-offsite" || op.Action == "delete") && (op.State == "running" || op.State == "unknown" || op.State == "unresolved") {
				retiring = true
			}
		}
		if !retiring && b.LocalState != "retiring" && b.State == "ready" && b.CleanupState == "complete" && b.Offsite != nil && b.Offsite.DeletedAt == nil && !b.Offsite.ConfirmedAt.IsZero() && b.Checksum != "" && b.Offsite.ManifestChecksum != "" && b.Offsite.StoreID == p.OffsiteStoreID && b.Offsite.VerificationState == "verified" && b.Offsite.VerifiedAt != nil {
			if p.LastOffsiteRecoveryPointAt == nil || b.CreatedAt.After(*p.LastOffsiteRecoveryPointAt) {
				point := b.CreatedAt
				p.LastOffsiteBackupID, p.LastOffsiteRecoveryPointAt, p.LastOffsiteVerifiedAt = b.ID, &point, b.Offsite.VerifiedAt
			}
		}
		if b.ID != p.LastBackupID {
			continue
		}
		if b.CleanupState != "complete" {
			p.OffsiteState, p.OffsiteMessage = "blocked", "The latest captured archive has incomplete cleanup. Reconcile its original operation before relying on offsite protection."
		} else if retiring || b.LocalState == "retiring" || b.Offsite != nil && b.Offsite.DeletedAt != nil {
			p.OffsiteState, p.OffsiteMessage = "blocked", "Archive retirement or removal is pending or confirmed. Inspect the retained copy history before unattended recovery."
		} else if b.Offsite != nil && b.Offsite.StoreID != p.OffsiteStoreID {
			p.OffsiteState, p.OffsiteMessage = "blocked", "The capture was exported to another destination. Inspect its original archive identity."
		} else if b.Offsite != nil {
			switch b.Offsite.VerificationState {
			case "verified":
				if b.State == "ready" && b.CleanupState == "complete" && b.Offsite.VerifiedAt != nil {
					p.OffsiteState, p.OffsiteMessage = "healthy", "The downloaded encrypted capture passed isolated offsite restore verification."
				} else {
					p.OffsiteState, p.OffsiteMessage = "blocked", "The latest captured archive has incomplete recovery evidence. Inspect its original operation before relying on offsite protection."
				}
			case "failed":
				p.OffsiteState, p.OffsiteMessage = "verification_failed", "Offsite restore verification failed. Earlier confirmed recovery points remain retained."
			case "unknown":
				p.OffsiteState, p.OffsiteMessage = "blocked", "Offsite verification or cleanup is uncertain. Reconcile its original operation."
			default:
				p.OffsiteState, p.OffsiteMessage = "verifying", "Exported bytes are awaiting isolated restore verification. Upload confirmation alone is not a recovery point."
			}
		} else if b.ScheduledExportOperationID == "" && b.ScheduledExportMissed {
			p.OffsiteState, p.OffsiteMessage = "export_failed", "Offsite export was not accepted. Inspect the approved destination, archive size and current authority."
		} else if b.ScheduledExportOperationID != "" {
			p.LastOffsiteOperationID = b.ScheduledExportOperationID
			for _, op := range operations[b.ID] {
				if op.ID != b.ScheduledExportOperationID {
					continue
				}
				switch op.State {
				case "running":
					p.OffsiteState, p.OffsiteMessage = "exporting", "The original encrypted capture export is in progress."
				case "failed":
					p.OffsiteState, p.OffsiteMessage = "export_failed", "Offsite export failed. Inspect the original operation before any explicit retry."
				case "unknown", "unresolved":
					p.OffsiteState, p.OffsiteMessage = "blocked", "Offsite export is uncertain. Reconcile the original export without repeating its mutation."
				}
			}
		}
	}
	return p
}

package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workloadbackup"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) workloadBackupPolicyRoutes(r chi.Router) {
	r.Get("/workload-backup-policies", a.listWorkloadBackupPolicies)
	r.Post("/workload-backup-policies", a.createWorkloadBackupPolicy)
	r.Get("/workload-backup-policies/{policyId}", a.getWorkloadBackupPolicy)
	r.Put("/workload-backup-policies/{policyId}", a.updateWorkloadBackupPolicy)
}
func (a *API) backupPolicyStore() (store.WorkloadBackupPolicyStore, error) {
	s, ok := a.store.(store.WorkloadBackupPolicyStore)
	if !ok {
		return nil, errors.New("durable capture policies are unavailable")
	}
	return s, nil
}
func (a *API) listWorkloadBackupPolicies(w http.ResponseWriter, r *http.Request) {
	s, err := a.backupPolicyStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	project := r.URL.Query().Get("projectId")
	if project != "" && !a.requireProject(w, r, core.PermissionProjectView, project) {
		return
	}
	items, err := s.ListWorkloadBackupPolicies(r.Context(), project)
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []core.WorkloadBackupPolicy{}
	for _, p := range items {
		if visible[p.ProjectID] || currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
			p = a.inspectBackupPolicyOffsite(r.Context(), p)
			result = append(result, p)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (a *API) getWorkloadBackupPolicy(w http.ResponseWriter, r *http.Request) {
	s, err := a.backupPolicyStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	p, err := s.GetWorkloadBackupPolicy(r.Context(), chi.URLParam(r, "policyId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Capture policy")
		return
	}
	if a.requireProject(w, r, core.PermissionProjectView, p.ProjectID) {
		p = a.inspectBackupPolicyOffsite(r.Context(), p)
		writeJSON(w, 200, p)
	}
}
func (a *API) createWorkloadBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RetireLocalAfterOffsiteVerification bool                        `json:"retireLocalAfterOffsiteVerification"`
		OffsiteStoreID                      string                      `json:"offsiteStoreId"`
		ConfirmOffsiteStoreID               string                      `json:"confirmOffsiteStoreId"`
		OffsiteStaleAfterHours              int                         `json:"offsiteStaleAfterHours"`
		NotificationAppID                   string                      `json:"notificationAppId"`
		Name                                string                      `json:"name"`
		SourceRunID                         string                      `json:"sourceRunId"`
		IntervalHours                       int                         `json:"intervalHours"`
		KeepLast                            int                         `json:"keepLast"`
		ConfirmRetention                    string                      `json:"confirmRetention"`
		Checks                              []core.BackupIntegrityCheck `json:"checks"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(strings.TrimSpace(input.Name)) < 2 || len(input.Name) > 80 || strings.ContainsAny(input.Name, "\x00\r\n") || input.IntervalHours < 1 || input.IntervalHours > 8760 || input.KeepLast < 1 || input.KeepLast > 1000 || input.ConfirmRetention != input.Name {
		problem(w, 422, "Invalid capture policy", "Choose a name, cadence between 1 and 8760 hours and between 1 and 1000 retained verified captures. Confirm automatic removal of older policy archives with the exact policy name.")
		return
	}
	if input.OffsiteStoreID == "" && (input.ConfirmOffsiteStoreID != "" || input.OffsiteStaleAfterHours != 0 || input.NotificationAppID != "" || input.RetireLocalAfterOffsiteVerification) || input.OffsiteStoreID != "" && input.ConfirmOffsiteStoreID != input.OffsiteStoreID || input.OffsiteStaleAfterHours != 0 && (input.OffsiteStaleAfterHours < input.IntervalHours || input.OffsiteStaleAfterHours > 17520) {
		problem(w, 422, "Explicit offsite approval required", "Choose an approved destination, confirm its exact ID and an optional freshness threshold between the capture interval and 17520 hours. Notifications require an offsite destination.")
		return
	}
	record, err := a.store.(store.ServiceResourceStore).GetServiceResource(r.Context(), input.SourceRunID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Owned PostgreSQL resource")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, record.ProjectID) || !a.requireProject(w, r, core.PermissionDeploymentRun, record.ProjectID) {
		return
	}
	if r.Header.Get("Idempotency-Key") == "" {
		problem(w, 422, "Idempotency key required", "Reuse an Idempotency-Key after an uncertain policy response.")
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, record.ProjectID, "workload.backup.policy.create", input, "workload_backup_policy", record.RunID)
	if !proceed {
		return
	}
	s, err := a.backupPolicyStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	id := receipt.OperationID
	now := time.Now().UTC()
	var p core.WorkloadBackupPolicy
	err = a.deploy.Storage.WithTarget(r.Context(), record.Target.ServerID, func() error {
		record, accepted, server, storage, err := a.backupSource(r.Context(), input.SourceRunID)
		if err != nil {
			return err
		}
		p = core.WorkloadBackupPolicy{ID: id, ProjectID: record.ProjectID, SourceRunID: record.RunID, SourceResourceID: record.ResourceID, ServerID: server.ID, NodeID: server.AgentNodeID, Name: input.Name, Enabled: true, Revision: 1, IntervalHours: input.IntervalHours, KeepLast: input.KeepLast, NextCaptureAt: now, State: "scheduled", Actor: currentIdentity(r.Context()), CreatedAt: now, UpdatedAt: now}
		p.OffsiteStoreID, p.OffsiteStaleAfterHours, p.NotificationAppID = input.OffsiteStoreID, input.OffsiteStaleAfterHours, input.NotificationAppID
		p.RetireLocalAfterOffsiteVerification = input.RetireLocalAfterOffsiteVerification
		if p.OffsiteStoreID != "" {
			if p.OffsiteStaleAfterHours == 0 {
				p.OffsiteStaleAfterHours = p.IntervalHours * 2
			}
			item, lookupErr := a.getPolicyOffsiteStore(r.Context(), p)
			if lookupErr != nil {
				return lookupErr
			}
			p.OffsiteStoreDigest = policyOffsiteStoreDigest(item)
			p.OffsiteState = "pending"
			p.OffsiteMessage = "The first captured recovery point is awaiting offsite export and verification."
			if p.NotificationAppID != "" {
				if _, lookupErr = a.backupPolicyNotificationConfig(r.Context(), p); lookupErr != nil {
					return lookupErr
				}
			}
		}
		if p.NodeID != "" {
			d, ok := a.store.(workloadBackupEnrollmentReader)
			if !ok {
				return errors.New("durable target identity is unavailable")
			}
			credential, err := d.GetEdgeCredential(r.Context(), p.NodeID)
			if err != nil || credential.Revoked || credential.PublicKey == "" {
				return errors.New("target enrollment is unavailable")
			}
			p.NodeGeneration = credential.Generation
		}
		key, err := workloadbackup.Key()
		if err != nil {
			return err
		}
		backup := core.WorkloadBackup{ID: id, ProjectID: p.ProjectID, SourceRunID: p.SourceRunID, StorageID: storage.ID, ServerID: p.ServerID, NodeID: p.NodeID, SourceResourceID: p.SourceResourceID, ArtifactID: id, Consistency: "database-native", Format: "postgresql-custom", Location: "target-local", Policy: "retain"}
		request := core.WorkloadBackupRequest{OperationID: id, Action: "backup", Backup: backup, Source: accepted.Request, Storage: storage, Key: key, Checks: input.Checks}
		if err = deploy.ValidateWorkloadBackupRequest(request, server); err != nil {
			return err
		}
		p.EncryptedInput, err = a.encryptWorkloadBackup(id, "policy", request)
		if err != nil {
			return err
		}
		if err = a.backupPolicyAuthority(r.Context(), p); err != nil {
			return err
		}
		return s.CreateWorkloadBackupPolicy(r.Context(), p)
	})
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Capture policy acceptance failed; inspect the source and policy ownership.")
		problem(w, 409, "Capture policy unavailable", "The source, storage, actor or existing policy changed. Inspect before retrying.")
		return
	}
	saved, err := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), receipt.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	a.writeMutationReceipt(w, r, saved, 201)
}
func (a *API) updateWorkloadBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Revision    int64  `json:"revision"`
		Enabled     *bool  `json:"enabled"`
		ConfirmName string `json:"confirmName"`
	}
	if !decode(w, r, &input) {
		return
	}
	s, err := a.backupPolicyStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	p, err := s.GetWorkloadBackupPolicy(r.Context(), chi.URLParam(r, "policyId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Capture policy")
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, p.ProjectID) || !a.requireProject(w, r, core.PermissionDeploymentRun, p.ProjectID) {
		return
	}
	if p.Revision != input.Revision || input.ConfirmName != p.Name {
		problem(w, 409, "Policy changed", "Confirm the current policy revision and exact name.")
		return
	}
	if input.Enabled == nil {
		problem(w, 422, "Explicit enabled value required", "Supply enabled as true or false when pausing or resuming a capture policy.")
		return
	}
	if *input.Enabled {
		err = a.backupPolicyAuthority(r.Context(), p)
		if err == nil {
			accepted, e := a.decryptWorkloadBackup(p.ID, "policy", p.EncryptedInput)
			err = e
			if err == nil {
				err = a.deploy.Storage.WithTarget(r.Context(), p.ServerID, func() error {
					record, _, _, storage, e := a.backupSource(r.Context(), p.SourceRunID)
					if e != nil {
						return e
					}
					if record.ResourceID != p.SourceResourceID || storage.ID != accepted.Storage.ID || storage.Identity != accepted.Storage.Identity || storage.Evidence != accepted.Storage.Evidence {
						return store.ErrWorkloadBackupChanged
					}
					return nil
				})
			}
		}
		if err != nil {
			problem(w, 409, "Policy authority changed", "The original actor, source or target needs recovery. Create a new reviewed policy if its frozen identity changed.")
			return
		}
	}
	p.Enabled = *input.Enabled
	if !p.Enabled {
		p.State = "paused"
		p.Message = "Automatic capture and retention are paused."
	} else {
		p.State = "scheduled"
		p.Message = "Automatic capture and retention resumed on the original cadence."
	}
	if err = s.UpdateWorkloadBackupPolicy(r.Context(), p, input.Revision); err != nil {
		problem(w, 409, "Policy changed", "Read the current policy before retrying.")
		return
	}
	p, _ = s.GetWorkloadBackupPolicy(r.Context(), p.ID)
	p = a.inspectBackupPolicyOffsite(r.Context(), p)
	writeJSON(w, 200, p)
}

// Reload the accepted policy at each execution boundary, including after waiting
// for the target lock. Manual operations use their own accepted authority.
func (a *API) backupOperationPolicyAuthority(ctx context.Context, op core.WorkloadBackupOperation) error {
	if op.CapturePolicyID == "" {
		return nil
	}
	policies, err := a.backupPolicyStore()
	if err != nil {
		return err
	}
	policy, err := policies.GetWorkloadBackupPolicy(ctx, op.CapturePolicyID)
	if err != nil || !policy.Enabled {
		return store.ErrWorkloadBackupChanged
	}
	return a.backupPolicyAuthority(ctx, policy)
}

// Each unattended mutation rechecks the original actor and frozen target enrollment.
func (a *API) backupPolicyAuthority(ctx context.Context, p core.WorkloadBackupPolicy) error {
	actor := p.Actor
	if actor.Kind == core.PrincipalServiceAccount {
		data, ok := a.store.(store.AutomationStore)
		if !ok {
			return store.ErrAutomationCredential
		}
		account, err := data.GetServiceAccount(ctx, actor.ID)
		if err != nil || account.State != "active" {
			return store.ErrAutomationCredential
		}
		credentials, err := data.ListAutomationCredentials(ctx, actor.ID)
		if err != nil {
			return err
		}
		valid := false
		for _, c := range credentials {
			if c.ID == actor.CredentialID && c.RevokedAt == nil && c.ExpiresAt.After(time.Now()) {
				valid = true
			}
		}
		if !valid {
			return store.ErrAutomationCredential
		}
		actor.SystemRole = core.UserRoleMember
	} else if actor.ID != "controller-owner" {
		user, err := a.store.GetUser(ctx, actor.ID)
		if err != nil || user.State != core.UserStateActive {
			return store.ErrAutomationCredential
		}
		actor = identityForUser(user)
	}
	ctx = withIdentity(ctx, actor)
	for _, permission := range []core.Permission{core.PermissionProjectView, core.PermissionProjectConfigure, core.PermissionDeploymentRun} {
		allowed, err := a.canProject(ctx, permission, p.ProjectID)
		if err != nil {
			return err
		}
		if !allowed {
			return store.ErrAutomationCredential
		}
	}
	assigned, err := a.assignedInfrastructure(ctx, p.ProjectID, "target", p.ServerID)
	if err != nil {
		return err
	}
	if !assigned {
		return store.ErrAutomationCredential
	}
	if p.OffsiteStoreID != "" {
		if _, err := a.getPolicyOffsiteStore(ctx, p); err != nil {
			return err
		}
	}
	return a.backupPolicyTarget(ctx, p)
}
func (a *API) backupPolicyTarget(ctx context.Context, p core.WorkloadBackupPolicy) error {
	target, err := a.store.GetServer(ctx, p.ServerID)
	if err != nil {
		return err
	}
	if target.AgentNodeID != p.NodeID {
		return store.ErrWorkloadBackupChanged
	}
	if p.NodeID != "" {
		data, ok := a.store.(workloadBackupEnrollmentReader)
		if !ok {
			return store.ErrWorkloadBackupChanged
		}
		credential, err := data.GetEdgeCredential(ctx, p.NodeID)
		if err != nil || credential.Revoked || credential.PublicKey == "" || credential.Generation != p.NodeGeneration {
			return store.ErrWorkloadBackupChanged
		}
	}
	return nil
}
func (a *API) scheduleWorkloadBackupCaptures(ctx context.Context) {
	s, err := a.backupPolicyStore()
	if err != nil {
		return
	}
	policies, err := s.ListWorkloadBackupPolicies(ctx, "")
	if err != nil {
		return
	}
	for _, p := range policies {
		if ctx.Err() != nil {
			return
		}
		if !p.Enabled {
			continue
		}
		a.recoverBackupPolicyOperations(ctx, p)
		p = a.refreshBackupPolicy(ctx, p)
		if err = a.backupPolicyAuthority(ctx, p); err != nil {
			p = a.scheduleBackupPolicyOffsite(ctx, p, false)
			a.missBackupCapture(ctx, p, "The original actor, source, destination or target no longer authorizes automatic capture and retention.")
			continue
		}
		if !p.NextCaptureAt.After(time.Now()) {
			if err = a.acceptBackupCapture(ctx, p); err != nil {
				a.missBackupCapture(ctx, p, "Fresh capture was not accepted. Inspect source ownership and pending or uncertain operations.")
			}
		}
		if latest, lookup := s.GetWorkloadBackupPolicy(ctx, p.ID); lookup == nil {
			p = latest
		}
		p = a.scheduleBackupPolicyOffsite(ctx, p, true)
		a.pruneBackupPolicy(ctx, p)
	}
}
func (a *API) missBackupCapture(ctx context.Context, p core.WorkloadBackupPolicy, message string) {
	s, _ := a.backupPolicyStore()
	now := time.Now().UTC()
	slot, next, missed := p.DueCapture(now)
	if !slot.IsZero() {
		p.LastScheduledAt = &slot
		p.NextCaptureAt = next
		p.MissedCaptures += missed + 1
		p.LastAttemptAt = &now
	}
	if p.State == "blocked" && p.Message == message && slot.IsZero() {
		return
	}
	p.State = "blocked"
	p.Message = message
	_ = s.UpdateWorkloadBackupPolicy(ctx, p, p.Revision)
}
func (a *API) acceptBackupCapture(ctx context.Context, p core.WorkloadBackupPolicy) error {
	s, _ := a.backupPolicyStore()
	stored, err := a.decryptWorkloadBackup(p.ID, "policy", p.EncryptedInput)
	if err != nil {
		return err
	}
	return a.deploy.Storage.WithTarget(ctx, p.ServerID, func() error {
		if err := a.backupPolicyAuthority(ctx, p); err != nil {
			return err
		}
		record, accepted, server, storage, err := a.backupSource(ctx, p.SourceRunID)
		if err != nil {
			return err
		}
		if record.ResourceID != p.SourceResourceID || storage.ID != stored.Storage.ID || storage.Identity != stored.Storage.Identity || storage.Evidence != stored.Storage.Evidence {
			return store.ErrWorkloadBackupChanged
		}
		now := time.Now().UTC()
		slot, _, _ := p.DueCapture(now)
		if slot.IsZero() {
			return store.ErrWorkloadBackupChanged
		}
		id := ulid.Make().String()
		key, err := workloadbackup.Key()
		if err != nil {
			return err
		}
		b := core.WorkloadBackup{ID: id, CapturePolicyID: p.ID, ScheduledAt: &slot, ProjectID: p.ProjectID, SourceRunID: p.SourceRunID, StorageID: storage.ID, ServerID: server.ID, NodeID: p.NodeID, SourceResourceID: p.SourceResourceID, State: "creating", Revision: 1, ArtifactID: id, Consistency: "database-native", Format: "postgresql-custom", Encryption: "AES-256-GCM-chunks-v1", KeyID: id, Location: "target-local", Policy: "retain", CheckCount: len(stored.Checks), VerificationState: "not_verified", CleanupState: "complete", VerificationIntervalHours: 24, CreatedAt: now, UpdatedAt: now}
		request := core.WorkloadBackupRequest{OperationID: id, Action: "backup", Backup: b, Source: accepted.Request, Storage: storage, Key: key, Checks: stored.Checks}
		if err = deploy.ValidateWorkloadBackupRequest(request, server); err != nil {
			return err
		}
		b.EncryptedInput, err = a.encryptWorkloadBackup(id, "accepted", request)
		if err != nil {
			return err
		}
		op := newBackupOperation(id, b, "backup")
		op.CapturePolicyID = p.ID
		op.EncryptedInput, err = a.encryptWorkloadBackup(id, "operation", request)
		if err != nil {
			return err
		}
		if err = s.AcceptWorkloadBackupCapture(ctx, p, b, op); err != nil {
			return err
		}
		go a.executeWorkloadBackupOperation(op, false)
		return nil
	})
}
func (a *API) refreshBackupPolicy(ctx context.Context, p core.WorkloadBackupPolicy) core.WorkloadBackupPolicy {
	if p.LastBackupID == "" {
		return p
	}
	s, _ := a.backupPolicyStore()
	backups, _ := a.workloadBackupStore()
	b, err := backups.GetWorkloadBackup(ctx, p.LastBackupID)
	if err != nil {
		return p
	}
	// A newer missed attempt must remain visible until another capture is accepted.
	if p.State == "blocked" && p.LastAttemptAt != nil && p.LastAttemptAt.After(b.CreatedAt) {
		return p
	}
	before := p.State
	oldVerified := p.LastVerifiedBackupID
	switch {
	case b.State == "failed":
		p.State = "capture_failed"
		p.Message = "Fresh capture failed. Previously verified archives remain retained."
	case b.State == "unknown":
		p.State = "blocked"
		p.Message = "Reconcile the original uncertain capture before accepting another."
	case b.State == "ready" && b.VerificationState == "verified" && b.CleanupState == "complete":
		p.State = "healthy"
		p.LastVerifiedBackupID = b.ID
		p.LastSuccessAt = b.VerifiedAt
		p.Message = "Fresh capture passed isolated restore verification."
	case b.State == "ready" && b.VerificationState == "failed":
		p.State = "verification_failed"
		p.Message = "Fresh archive failed verification. Older verified archives remain retained."
	case b.State == "ready" && b.VerificationState == "unknown":
		p.State = "blocked"
		p.Message = "Reconcile uncertain verification and cleanup before continuing."
	case b.State == "ready":
		p.State = "verifying"
		p.Message = "Fresh archive is awaiting isolated verification."
	}
	if before != p.State || oldVerified != p.LastVerifiedBackupID {
		if err = s.UpdateWorkloadBackupPolicy(ctx, p, p.Revision); err == nil {
			p.Revision++
		}
	}
	return p
}
func (a *API) pruneBackupPolicy(ctx context.Context, p core.WorkloadBackupPolicy) {
	if p.OffsiteStoreID != "" && (!p.RetireLocalAfterOffsiteVerification || p.LastOffsiteBackupID != p.LastBackupID) || p.State != "healthy" || p.LastBackupID == "" || p.LastBackupID != p.LastVerifiedBackupID {
		return
	}
	s, _ := a.backupPolicyStore()
	backups, _ := a.workloadBackupStore()
	items, err := backups.ListWorkloadBackups(ctx, p.ProjectID)
	if err != nil {
		return
	}
	verified := []core.WorkloadBackup{}
	for _, b := range items {
		if b.CapturePolicyID == p.ID && b.LocalState != "retired" && b.LocalState != "retiring" && b.State == "ready" && b.VerificationState == "verified" && b.CleanupState == "complete" {
			verified = append(verified, b)
		}
	}
	sort.Slice(verified, func(i, j int) bool {
		if verified[i].CreatedAt.Equal(verified[j].CreatedAt) {
			return verified[i].ID > verified[j].ID
		}
		return verified[i].CreatedAt.After(verified[j].CreatedAt)
	})
	if len(verified) <= p.KeepLast {
		return
	}
	// One reviewed-policy archive per tick keeps retention bounded and restartable.
	var b core.WorkloadBackup
	for i := len(verified) - 1; i >= p.KeepLast; i-- {
		if p.OffsiteStoreID == "" && verified[i].Offsite == nil || p.OffsiteStoreID != "" && verifiedPolicyOffsite(p, verified[i]) {
			b = verified[i]
			break
		}
	}
	if b.ID == "" {
		return
	}
	request, err := a.decryptWorkloadBackup(b.ID, "accepted", b.EncryptedInput)
	if err != nil {
		return
	}
	action := "delete"
	if p.OffsiteStoreID != "" {
		action = "retire-local"
	}
	op := newBackupOperation(ulid.Make().String(), b, action)
	op.CapturePolicyID = p.ID
	request.OperationID, request.Action, request.Backup = op.ID, action, b
	if action == "retire-local" {
		access, grantErr := a.grantBackupObjects(ctx, b, p.OffsiteStoreID, false, false)
		if grantErr != nil {
			return
		}
		request.OffsiteAccess, request.ExecutionNodeGeneration = &access, p.NodeGeneration
		op.OffsiteStoreID = p.OffsiteStoreID
		op.ExecutionServerID, op.ExecutionNodeID, op.ExecutionGeneration = p.ServerID, p.NodeID, p.NodeGeneration
	}
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", request)
	if err != nil {
		return
	}
	if err = s.AcceptWorkloadBackupRetention(ctx, p, b, op); err == nil {
		go a.executeWorkloadBackupOperation(op, false)
	}
}

// After restart, inspect the original accepted operation rather than repeat its mutation.
func (a *API) recoverBackupPolicyOperations(ctx context.Context, p core.WorkloadBackupPolicy) {
	if a.backupPolicyAuthority(ctx, p) != nil {
		return
	}
	backups, _ := a.workloadBackupStore()
	items, err := backups.ListWorkloadBackups(ctx, p.ProjectID)
	if err != nil {
		return
	}
	for _, b := range items {
		if b.CapturePolicyID != p.ID {
			continue
		}
		ops, err := backups.ListWorkloadBackupOperations(ctx, b.ID)
		if err != nil {
			return
		}
		for _, op := range ops {
			if op.CapturePolicyID != p.ID || (op.State != "running" && op.State != "unknown") || op.LeaseUntil.After(time.Now()) {
				continue
			}
			claimed, err := backups.ClaimWorkloadBackupRecovery(ctx, op.ID, time.Now().UTC(), ulid.Make().String())
			if err == nil {
				go a.executeWorkloadBackupOperation(claimed, true)
			}
			return
		}
	}
}

// Called before agent input release and lease renewal for unattended policy jobs.
func (a *API) checkBackupPolicyRuntimeAuthority(ctx context.Context, id string) error {
	operationID := ""
	var binding *remoteruntime.BackupAuthorityBinding
	if broker := a.runtimeBroker(); broker != nil {
		var bindingErr error
		binding, bindingErr = broker.BackupAuthority(ctx, id)
		if bindingErr != nil && !errors.Is(bindingErr, store.ErrNotFound) {
			return bindingErr
		}
		if binding != nil {
			operationID = binding.OperationID
		}
	}
	// The prefix supports existing directly queued capture records. Runtime-backed
	// jobs always resolve the operation from their authenticated encrypted request.
	if operationID == "" && strings.HasPrefix(id, "backup-") {
		operationID = strings.TrimPrefix(id, "backup-")
	}
	if operationID == "" {
		return nil
	}
	backups, err := a.workloadBackupStore()
	if err != nil {
		return err
	}
	op, err := backups.GetWorkloadBackupOperation(ctx, operationID)
	if errors.Is(err, store.ErrNotFound) && binding == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if binding != nil {
		if op.BackupID != binding.BackupID || op.ProjectID != binding.ProjectID || op.TargetRunID != binding.TargetRunID || op.OffsiteStoreID != binding.OffsiteStoreID {
			return store.ErrWorkloadBackupChanged
		}
		backup, lookupErr := backups.GetWorkloadBackup(ctx, op.BackupID)
		if lookupErr != nil {
			return lookupErr
		}
		if backup.SourceRunID != binding.SourceRunID || backup.SourceResourceID != binding.SourceResourceID {
			return store.ErrWorkloadBackupChanged
		}
		serverID, nodeID := backup.ServerID, backup.NodeID
		if op.ExecutionServerID != "" {
			serverID, nodeID = op.ExecutionServerID, op.ExecutionNodeID
		}
		if serverID != binding.ServerID || nodeID != binding.NodeID {
			return store.ErrWorkloadBackupChanged
		}
		data, ok := a.store.(workloadBackupEnrollmentReader)
		if !ok {
			return store.ErrWorkloadBackupChanged
		}
		credential, lookupErr := data.GetEdgeCredential(ctx, binding.NodeID)
		if lookupErr != nil || credential.Revoked || credential.PublicKey == "" || credential.Generation != binding.NodeGeneration {
			return store.ErrWorkloadBackupChanged
		}
		if op.ExecutionGeneration != 0 && op.ExecutionGeneration != binding.NodeGeneration {
			return store.ErrWorkloadBackupChanged
		}
	}
	if op.CapturePolicyID == "" {
		return nil
	}
	policies, err := a.backupPolicyStore()
	if err != nil {
		return err
	}
	p, err := policies.GetWorkloadBackupPolicy(ctx, op.CapturePolicyID)
	if err != nil {
		return err
	}
	if !p.Enabled {
		return store.ErrWorkloadBackupChanged
	}
	if err = a.backupPolicyAuthority(ctx, p); err != nil {
		return err
	}
	return nil
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workloadbackup"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type workloadBackupRuntime func(context.Context, core.WorkloadBackupRequest, core.Server) (core.WorkloadBackupResult, error)

func (a *API) workloadBackupStore() (store.WorkloadBackupStore, error) {
	s, ok := a.store.(store.WorkloadBackupStore)
	if !ok {
		return nil, errors.New("durable workload backup storage is unavailable")
	}
	return s, nil
}
func (a *API) workloadBackupRoutes(r chi.Router) {
	a.workloadBackupPolicyRoutes(r)
	a.workloadBackupOffsiteRoutes(r)
	r.Get("/workload-backups", a.listWorkloadBackups)
	r.Get("/workload-backup-operations/{operationId}", a.getWorkloadBackupOperation)
	r.Post("/workload-backups", a.createWorkloadBackup)
	r.Route("/workload-backups/{id}", func(r chi.Router) {
		r.Use(a.workloadBackupPermission(core.PermissionProjectView))
		r.Get("/", a.getWorkloadBackup)
		r.Get("/operations", a.listWorkloadBackupOperations)
		r.Group(func(r chi.Router) {
			r.Use(a.workloadBackupPermission(core.PermissionProjectConfigure))
			r.Post("/verify", a.verifyWorkloadBackup)
			r.Post("/export", a.exportWorkloadBackup)
			r.Post("/operations/{operationId}/reconcile", a.reconcileWorkloadBackup)
			r.Post("/delete-preview", a.previewDestructiveAction("workload-backup", "delete"))
			r.Post("/delete", a.deleteWorkloadBackup)
			r.Post("/retire-local-preview", a.previewDestructiveAction("workload-backup", "retire-local"))
			r.Post("/retire-local", a.retireLocalWorkloadBackup)
			r.Post("/delete-offsite-preview", a.previewDestructiveAction("workload-backup", "delete-offsite"))
			r.Post("/delete-offsite", a.deleteOffsiteWorkloadBackup)
			r.Post("/restore/{destinationId}/preview", a.previewDestructiveAction("workload-backup", "restore"))
			r.Post("/restore/{destinationId}", a.restoreWorkloadBackup)
		})
	})
}
func (a *API) workloadBackupPermission(permission core.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s, err := a.workloadBackupStore()
			if err != nil {
				a.internal(w, err)
				return
			}
			b, err := s.GetWorkloadBackup(r.Context(), chi.URLParam(r, "id"))
			if err != nil {
				a.notFoundOrInternal(w, err, "Workload backup")
				return
			}
			if a.requireProject(w, r, permission, b.ProjectID) {
				next.ServeHTTP(w, r)
			}
		})
	}
}
func (a *API) listWorkloadBackups(w http.ResponseWriter, r *http.Request) {
	s, err := a.workloadBackupStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	project := r.URL.Query().Get("projectId")
	if project != "" && !a.requireProject(w, r, core.PermissionProjectView, project) {
		return
	}
	items, err := s.ListWorkloadBackups(r.Context(), project)
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []core.WorkloadBackup{}
	for _, b := range items {
		if visible[b.ProjectID] || currentIdentity(r.Context()).SystemRole == core.UserRoleOwner {
			result = append(result, b)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (a *API) getWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	s, _ := a.workloadBackupStore()
	b, err := s.GetWorkloadBackup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workload backup")
		return
	}
	writeJSON(w, 200, b)
}
func (a *API) listWorkloadBackupOperations(w http.ResponseWriter, r *http.Request) {
	s, _ := a.workloadBackupStore()
	items, err := s.ListWorkloadBackupOperations(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) encryptWorkloadBackup(id, kind string, input core.WorkloadBackupRequest) (string, error) {
	if a.eventConfig.Vault == nil {
		return "", errors.New("backup encryption is unavailable")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	return a.eventConfig.Vault.Encrypt("workload-backup:"+id+":"+kind, raw)
}
func (a *API) decryptWorkloadBackup(id, kind, encrypted string) (core.WorkloadBackupRequest, error) {
	var input core.WorkloadBackupRequest
	if a.eventConfig.Vault == nil {
		return input, errors.New("backup encryption is unavailable")
	}
	raw, err := a.eventConfig.Vault.Decrypt("workload-backup:"+id+":"+kind, encrypted)
	if err != nil {
		return input, errors.New("backup recovery material cannot be decrypted")
	}
	defer clear(raw)
	err = json.Unmarshal(raw, &input)
	return input, err
}
func (a *API) backupOwnedStorage(ctx context.Context, record core.ServiceResource) (core.StorageResource, error) {
	items, err := a.store.ListStorage(ctx, record.Target.ServerID)
	if err != nil {
		return core.StorageResource{}, err
	}
	var found *core.StorageResource
	for _, item := range items {
		if item.ProvisionRunID == record.RunID && item.ProjectID == record.ProjectID && item.Ownership == "verified" && item.Kind == "docker_volume" && item.State == "present" {
			if found != nil {
				return item, errors.New("backup source has multiple volumes; native PostgreSQL requires one owned data volume")
			}
			copy := item
			found = &copy
		}
	}
	if found == nil {
		return core.StorageResource{}, errors.New("backup requires one verified PostgreSQL data volume")
	}
	return *found, nil
}
func (a *API) backupSource(ctx context.Context, id string) (core.ServiceResource, acceptedServiceResource, core.Server, core.StorageResource, error) {
	record, err := a.store.(store.ServiceResourceStore).GetServiceResource(ctx, id)
	if err != nil {
		return record, acceptedServiceResource{}, core.Server{}, core.StorageResource{}, err
	}
	accepted, server, err := a.loadAcceptedServiceResource(ctx, record)
	if err != nil {
		return record, accepted, server, core.StorageResource{}, err
	}
	if record.State != "ready" || record.Target.Provider != "docker" || accepted.Request.ServiceType != "postgresql" || record.ResourceID == "" {
		return record, accepted, server, core.StorageResource{}, errors.New("native backups require a ready owned PostgreSQL service on Docker")
	}
	if err = a.deploy.Storage.RefreshLocked(ctx, server); err != nil {
		return record, accepted, server, core.StorageResource{}, err
	}
	storage, err := a.backupOwnedStorage(ctx, record)
	return record, accepted, server, storage, err
}
func newBackupOperation(id string, b core.WorkloadBackup, action string) core.WorkloadBackupOperation {
	now := time.Now().UTC()
	return core.WorkloadBackupOperation{ID: id, BackupID: b.ID, ProjectID: b.ProjectID, Action: action, State: "running", Revision: 1, LeaseToken: ulid.Make().String(), LeaseUntil: now.Add(31 * time.Minute), CreatedAt: now, UpdatedAt: now}
}
func (a *API) createWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SourceRunID               string                      `json:"sourceRunId"`
		Checks                    []core.BackupIntegrityCheck `json:"checks"`
		VerificationIntervalHours int                         `json:"verificationIntervalHours"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.VerificationIntervalHours < 0 || input.VerificationIntervalHours > 8760 {
		problem(w, 422, "Invalid verification schedule", "Use zero for manual verification or 1 to 8760 hours.")
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
	r, receipt, proceed := a.reserveMutation(w, r, record.ProjectID, "workload.backup.create", input, "workload_backup", record.RunID)
	if !proceed {
		return
	}
	id := ulid.Make().String()
	if receipt != nil {
		id = receipt.OperationID
	}
	s, err := a.workloadBackupStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	var b core.WorkloadBackup
	var op core.WorkloadBackupOperation
	err = a.deploy.Storage.WithTarget(r.Context(), record.Target.ServerID, func() error {
		record, accepted, server, storage, err := a.backupSource(r.Context(), input.SourceRunID)
		if err != nil {
			return err
		}
		key, err := workloadbackup.Key()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		b = core.WorkloadBackup{ID: id, ProjectID: record.ProjectID, SourceRunID: record.RunID, StorageID: storage.ID, ServerID: server.ID, NodeID: server.AgentNodeID, SourceResourceID: record.ResourceID, State: "creating", Revision: 1, ArtifactID: id, Consistency: "database-native", Format: "postgresql-custom", Encryption: "AES-256-GCM-chunks-v1", KeyID: id, Location: "target-local", Policy: "retain", CheckCount: len(input.Checks), VerificationState: "not_verified", CleanupState: "complete", VerificationIntervalHours: input.VerificationIntervalHours, CreatedAt: now, UpdatedAt: now}
		request := core.WorkloadBackupRequest{OperationID: id, Action: "backup", Backup: b, Source: accepted.Request, Storage: storage, Key: key, Checks: input.Checks}
		if err = deploy.ValidateWorkloadBackupRequest(request, server); err != nil {
			return err
		}
		b.EncryptedInput, err = a.encryptWorkloadBackup(id, "accepted", request)
		if err != nil {
			return err
		}
		op = newBackupOperation(id, b, "backup")
		op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", request)
		if err != nil {
			return err
		}
		return s.CreateWorkloadBackup(r.Context(), b, op)
	})
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Backup acceptance failed; inspect the owned source, encryption and storage.")
		problem(w, 409, "Backup unavailable", err.Error())
		return
	}
	core.RecordAcceptedOperation(r.Context(), op.ID)
	go a.executeWorkloadBackupOperation(op, false)
	a.writeBackupAcceptance(w, r, op, receipt)
}
func (a *API) writeBackupAcceptance(w http.ResponseWriter, r *http.Request, op core.WorkloadBackupOperation, receipt *core.MutationReceipt) {
	if receipt != nil {
		saved, err := a.store.(store.MutationReceiptStore).GetMutationReceipt(r.Context(), receipt.ID)
		if err != nil {
			a.internal(w, err)
			return
		}
		a.writeMutationReceipt(w, r, saved, 202)
		return
	}
	writeJSON(w, 202, op)
}
func (a *API) verifyWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	s, _ := a.workloadBackupStore()
	b, err := s.GetWorkloadBackup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, b.ProjectID) {
		return
	}
	var input struct {
		DestinationRunID string `json:"destinationRunId"`
	}
	if r.Body != nil {
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			problem(w, 422, "Invalid verification input", "Supply an owned destination run ID.")
			return
		}
	}
	r, receipt, proceed := a.reserveMutation(w, r, b.ProjectID, "workload.backup.verify", struct{ ID, Destination string }{b.ID, input.DestinationRunID}, "workload_backup", b.ID)
	if !proceed {
		return
	}
	id := ulid.Make().String()
	if receipt != nil {
		id = receipt.OperationID
	}
	op, err := a.acceptWorkloadBackupOperation(r.Context(), b, "verify", id, input.DestinationRunID)
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Verification was not accepted; inspect the original backup operation.")
		problem(w, 409, "Verification unavailable", err.Error())
		return
	}
	core.RecordAcceptedOperation(r.Context(), op.ID)
	go a.executeWorkloadBackupOperation(op, false)
	a.writeBackupAcceptance(w, r, op, receipt)
}
func (a *API) acceptWorkloadBackupOperation(ctx context.Context, b core.WorkloadBackup, action, id, destination string, policyIDs ...string) (core.WorkloadBackupOperation, error) {
	op := newBackupOperation(id, b, action)
	if len(policyIDs) > 0 {
		op.CapturePolicyID = policyIDs[0]
	}
	input, err := a.decryptWorkloadBackup(b.ID, "accepted", b.EncryptedInput)
	if err != nil {
		return op, err
	}
	input.Backup = b
	if (action == "restore" || action == "verify") && b.LocalState == "retired" && (b.Offsite == nil || b.Offsite.DeletedAt != nil) {
		return op, store.ErrWorkloadBackupChanged
	}
	input.OperationID, input.Action = id, action
	if action == "restore" || action == "verify" && destination != "" {
		record, accepted, server, storage, err := a.backupSource(ctx, destination)
		if err != nil {
			return op, err
		}
		if record.ProjectID != b.ProjectID || server.ID != b.ServerID && b.Offsite == nil {
			return op, errors.New("recovery requires an owned destination in the same project and an exported archive for another target")
		}
		input.Destination, input.DestinationStorage, input.ExpectedDestination = &accepted.Request, &storage, record.ResourceID
		op.TargetRunID, op.TargetName, op.TargetResourceID = record.RunID, record.Name, record.ResourceID
		op.ExecutionServerID = server.ID
		op.ExecutionNodeID = server.AgentNodeID
		if server.AgentNodeID != "" {
			data, ok := a.store.(workloadBackupEnrollmentReader)
			if !ok {
				return op, errors.New("target identity unavailable")
			}
			credential, e := data.GetEdgeCredential(ctx, server.AgentNodeID)
			if e != nil || credential.Revoked || credential.PublicKey == "" {
				return op, errors.New("destination enrollment is unavailable")
			}
			op.ExecutionGeneration = credential.Generation
			input.ExecutionNodeGeneration = credential.Generation
		}
	}
	if b.Offsite != nil && b.Offsite.DeletedAt == nil && (action == "verify" || action == "restore" || action == "retire-local" || action == "delete-offsite") {
		access, grantErr := a.grantBackupObjects(ctx, b, b.Offsite.StoreID, false, action == "delete-offsite")
		input.OffsiteAccess, err = &access, grantErr
		if err != nil {
			return op, err
		}
		op.OffsiteStoreID = b.Offsite.StoreID
		if op.ExecutionServerID == "" && action != "delete-offsite" {
			server, e := a.store.GetServer(ctx, b.ServerID)
			if e != nil || server.AgentNodeID != b.NodeID {
				return op, errors.New("accepted source target changed")
			}
			op.ExecutionServerID, op.ExecutionNodeID = server.ID, server.AgentNodeID
			if server.AgentNodeID != "" {
				data, ok := a.store.(workloadBackupEnrollmentReader)
				if !ok {
					return op, errors.New("target identity unavailable")
				}
				credential, e := data.GetEdgeCredential(ctx, server.AgentNodeID)
				if e != nil || credential.Revoked || credential.PublicKey == "" {
					return op, errors.New("target enrollment unavailable")
				}
				op.ExecutionGeneration = credential.Generation
				input.ExecutionNodeGeneration = credential.Generation
			}
		}
	}
	op.EncryptedInput, err = a.encryptWorkloadBackup(op.ID, "operation", input)
	if err != nil {
		return op, err
	}
	s, _ := a.workloadBackupStore()
	return op, s.CreateWorkloadBackupOperation(ctx, op, b.Revision)
}
func (a *API) deleteWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	a.mutateWorkloadBackup(w, r, "delete")
}
func (a *API) restoreWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	a.mutateWorkloadBackup(w, r, "restore")
}
func (a *API) mutateWorkloadBackup(w http.ResponseWriter, r *http.Request, action string) {
	if (action == "retire-local" || action == "delete-offsite") && strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		problem(w, 422, "Idempotency key required", "Choose one stable key for this reviewed backup operation.")
		return
	}
	s, _ := a.workloadBackupStore()
	b, err := s.GetWorkloadBackup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, b.ProjectID) {
		return
	}
	var body struct {
		Confirmation destructiveConfirmation `json:"confirmation"`
	}
	if !decode(w, r, &body) {
		return
	}
	input := struct {
		Confirmation destructiveConfirmation
		Destination  string
	}{body.Confirmation, chi.URLParam(r, "destinationId")}
	r, receipt, proceed := a.reserveMutation(w, r, b.ProjectID, "workload.backup."+action, input, "workload_backup", b.ID)
	if !proceed {
		return
	}
	raw, _ := json.Marshal(body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	r.ContentLength = int64(len(raw))
	accepted := false
	a.confirmDestructiveAction("workload-backup", action, func(w http.ResponseWriter, r *http.Request) {
		var op core.WorkloadBackupOperation
		err := a.deploy.Storage.WithTarget(r.Context(), b.ServerID, func() error {
			if err := a.recheckDestructiveAction(r, "workload-backup", action); err != nil {
				return err
			}
			fresh, err := s.GetWorkloadBackup(r.Context(), b.ID)
			if err != nil {
				return err
			}
			id := ulid.Make().String()
			if receipt != nil {
				id = receipt.OperationID
			}
			op, err = a.acceptWorkloadBackupOperation(r.Context(), fresh, action, id, chi.URLParam(r, "destinationId"))
			return err
		})
		if err != nil {
			problem(w, 409, "Backup action changed", "Inspect the backup, destination and active operations, then review again.")
			return
		}
		accepted = true
		core.RecordAcceptedOperation(r.Context(), op.ID)
		go a.executeWorkloadBackupOperation(op, false)
		a.writeBackupAcceptance(w, r, op, receipt)
	}).ServeHTTP(w, r)
	if !accepted {
		a.failMutationAcceptance(r.Context(), 409, "The backup action was rejected; review its current consequences.")
	}
}
func (a *API) workloadBackupReview(ctx context.Context, r *http.Request, action string) (destructiveReview, error) {
	id := chi.URLParam(r, "id")
	out := destructiveReview{ResourceID: id, ResourceType: "workload-backup", Action: action, Resources: []string{}}
	s, err := a.workloadBackupStore()
	if err != nil {
		return out, err
	}
	b, err := s.GetWorkloadBackup(ctx, id)
	if err != nil {
		return out, err
	}
	out.Name, out.ProjectID, out.StoragePolicy = b.ID, b.ProjectID, "retain"
	operations, err := s.ListWorkloadBackupOperations(ctx, id)
	if err != nil {
		return out, err
	}
	for _, op := range operations {
		if op.State == "running" || op.State == "unknown" {
			out.BlockedReason = "Reconcile the active or uncertain backup operation first."
		}
	}
	if b.State == "deleted" {
		out.BlockedReason = "The archive is already deleted."
	}
	if action == "retire-local" || action == "delete-offsite" {
		return a.workloadBackupRetirementReview(ctx, b, action, out)
	}
	var target any
	if action == "restore" {
		record, accepted, server, storage, err := a.backupSource(ctx, chi.URLParam(r, "destinationId"))
		if err != nil {
			return out, err
		}
		_ = accepted
		if record.ProjectID != b.ProjectID || b.Offsite == nil && (server.ID != b.ServerID || server.AgentNodeID != b.NodeID) {
			return out, errors.New("restore destination is outside the backup project or original target")
		}
		busy, err := a.store.(store.ServiceResourceStore).ServiceResourceConsumers(ctx, record.ServiceID)
		if err != nil {
			return out, err
		}
		if busy {
			out.BlockedReason = "Detach and redeploy consumers before restoring database objects."
		}
		if record.LeaseUntil.After(time.Now()) {
			out.BlockedReason = "The destination has an active resource operation."
		}
		if b.State != "ready" || b.Checksum == "" {
			out.BlockedReason = "A complete authenticated archive is required."
		}
		storage.ObservedAt = time.Time{}
		target = struct {
			Resource core.ServiceResource
			Storage  core.StorageResource
		}{record, storage}
		out.Name = record.Name
		out.Summary = "Overwrite database objects present in this backup in one PostgreSQL transaction. Objects absent from the archive remain. Detach consumers first; no deployment rollback is triggered."
		out.Resources = []string{"Destination: " + record.Name, "Target: " + server.Name, "Archive: " + b.ID, "Retain the encrypted backup after restore"}
	} else {
		if b.Offsite != nil && b.Offsite.DeletedAt == nil {
			out.BlockedReason = "Retire the local copy or review offsite deletion separately before deleting all local archive metadata."
		}
		if b.CapturePolicyID != "" && b.VerificationState == "verified" {
			policies, e := a.backupPolicyStore()
			if e != nil {
				return out, e
			}
			p, e := policies.GetWorkloadBackupPolicy(ctx, b.CapturePolicyID)
			if e != nil {
				return out, e
			}
			if p.Enabled {
				out.BlockedReason = "Pause the capture policy before deleting a protected verified archive. Automatic retention separately preserves the policy recovery points."
			}
		}
		cohort, e := s.ListWorkloadBackups(ctx, b.ProjectID)
		if e != nil {
			return out, e
		}
		points := []core.WorkloadBackup{}
		for _, other := range cohort {
			if other.SourceRunID == b.SourceRunID {
				points = append(points, other)
			}
		}
		var currentPolicy any
		if b.CapturePolicyID != "" {
			policies, e := a.backupPolicyStore()
			if e != nil {
				return out, e
			}
			currentPolicy, e = policies.GetWorkloadBackupPolicy(ctx, b.CapturePolicyID)
			if e != nil {
				return out, e
			}
		}
		target = struct {
			RecoveryPoints []core.WorkloadBackup
			Policy         any
		}{points, currentPolicy}
		out.Summary = "Permanently delete this retained workload backup archive. Workload data and encrypted operation history remain."
		out.StoragePolicy = "destroy"
		out.Resources = []string{"Encrypted archive: " + b.ID, "Target-local bytes: " + fmt.Sprint(b.Bytes), "This backup will no longer protect target deletion"}
	}
	out.Version = mutationHash(struct {
		Backup core.WorkloadBackup
		Target any
		Action string
	}{b, target, action})
	return out, nil
}
func (a *API) reconcileWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	s, _ := a.workloadBackupStore()
	b, err := s.GetWorkloadBackup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, b.ProjectID) {
		return
	}
	op, err := s.GetWorkloadBackupOperation(r.Context(), chi.URLParam(r, "operationId"))
	if err != nil || op.BackupID != b.ID {
		problem(w, 404, "Operation not found", "No operation exists in this backup.")
		return
	}
	op, err = s.ClaimWorkloadBackupRecovery(r.Context(), op.ID, time.Now().UTC(), ulid.Make().String())
	if err != nil {
		problem(w, 409, "Recovery unavailable", "Wait until the original execution lease ends, then inspect and reconcile its outcome.")
		return
	}
	core.RecordAcceptedOperation(r.Context(), op.ID)
	go a.executeWorkloadBackupOperation(op, true)
	writeJSON(w, 202, op)
}

// Verification scheduling claims the existing operation table; no additional job queue.
func (a *API) RunWorkloadBackupVerification(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		a.scheduleWorkloadBackupCaptures(ctx)
		a.scheduleWorkloadBackupVerification(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (a *API) scheduleWorkloadBackupVerification(ctx context.Context) {
	s, err := a.workloadBackupStore()
	if err != nil {
		return
	}
	items, err := s.ListWorkloadBackups(ctx, "")
	if err != nil {
		return
	}
	for _, b := range items {
		if ctx.Err() != nil {
			return
		}
		if b.State != "ready" || b.VerificationIntervalHours == 0 || b.NextVerificationAt == nil || b.NextVerificationAt.After(time.Now()) {
			continue
		}
		policyID := ""
		if b.CapturePolicyID != "" {
			policies, e := a.backupPolicyStore()
			if e != nil {
				continue
			}
			policy, e := policies.GetWorkloadBackupPolicy(ctx, b.CapturePolicyID)
			if e != nil || !policy.Enabled || a.backupPolicyAuthority(ctx, policy) != nil {
				continue
			}
			policyID = policy.ID
		}
		op, err := a.acceptWorkloadBackupOperation(ctx, b, "verify", ulid.Make().String(), "", policyID)
		if err == nil {
			go a.executeWorkloadBackupOperation(op, false)
		}
	}
}

func (a *API) getWorkloadBackupOperation(w http.ResponseWriter, r *http.Request) {
	s, err := a.workloadBackupStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	op, err := s.GetWorkloadBackupOperation(r.Context(), chi.URLParam(r, "operationId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Backup operation")
		return
	}
	if a.requireProject(w, r, core.PermissionProjectView, op.ProjectID) {
		writeJSON(w, 200, op)
	}
}

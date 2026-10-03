package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) backupObjectStore() (store.BackupObjectStoreStore, error) {
	s, ok := a.store.(store.BackupObjectStoreStore)
	if !ok {
		return nil, errors.New("offsite destination storage unavailable")
	}
	return s, nil
}
func (a *API) workloadBackupOffsiteRoutes(r chi.Router) {
	r.Get("/workload-backup-stores", a.listBackupObjectStores)
	r.Get("/workload-backup-stores/{storeId}", a.getBackupObjectStore)
	r.Group(func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/workload-backup-stores", a.createBackupObjectStore)
		r.Put("/workload-backup-stores/{storeId}/conditional-delete", a.setBackupObjectStoreConditionalDelete)
	})
}
func (a *API) createBackupObjectStore(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ProjectID          string             `json:"projectId"`
		Name               string             `json:"name"`
		CredentialSecretID string             `json:"credentialSecretId"`
		Config             backupstore.Config `json:"config"`
	}
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || len(input.Name) > 80 || strings.ContainsAny(input.Name, "\r\n\x00") || input.Config.Validate() != nil {
		problem(w, 422, "Invalid offsite destination", "Choose a project, name, bare HTTPS S3 endpoint, bucket, region, prefix and a size limit of at most 4 GiB.")
		return
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	item := core.BackupObjectStore{ID: ulid.Make().String(), ProjectID: input.ProjectID, Name: input.Name, CredentialSecretID: input.CredentialSecretID, Config: input.Config, CreatedAt: time.Now().UTC()}
	if _, err := a.objectStoreCredentials(r.Context(), item); err != nil {
		problem(w, 422, "Offsite credential unavailable", "Choose a write-only secret with valid scoped S3 credential JSON.")
		return
	}
	s, err := a.backupObjectStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	if err = s.CreateBackupObjectStore(r.Context(), item); err != nil {
		problem(w, 409, "Offsite registration failed", "Inspect the destination project and credential reference.")
		return
	}
	writeJSON(w, 201, item)
}
func (a *API) objectStoreCredentials(ctx context.Context, item core.BackupObjectStore) (backupstore.Credentials, error) {
	var credential backupstore.Credentials
	if a.secretResolver == nil {
		return credential, errors.New("secret resolution unavailable")
	}
	secret, err := a.store.GetSecret(ctx, item.CredentialSecretID)
	if err != nil || secret.Type != core.SecretTypeJSON {
		return credential, errors.New("offsite credentials require a write-only JSON secret")
	}
	raw, err := a.secretResolver.Resolve(ctx, item.CredentialSecretID)
	if err != nil {
		return credential, errors.New("offsite credential unavailable")
	}
	defer clear(raw)
	if err = json.Unmarshal(raw, &credential); err != nil {
		return credential, errors.New("offsite credential invalid")
	}
	return credential, credential.Validate()
}
func (a *API) listBackupObjectStores(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("projectId")
	if project != "" && !a.requireProject(w, r, core.PermissionProjectView, project) {
		return
	}
	s, err := a.backupObjectStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	items, err := s.ListBackupObjectStores(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []core.BackupObjectStore{}
	for _, item := range items {
		if project != "" && item.ProjectID != project {
			continue
		}
		if currentIdentity(r.Context()).SystemRole == core.UserRoleOwner || visible[item.ProjectID] {
			result = append(result, item)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (a *API) getBackupObjectStore(w http.ResponseWriter, r *http.Request) {
	s, err := a.backupObjectStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	item, err := s.GetBackupObjectStore(r.Context(), chi.URLParam(r, "storeId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Offsite destination")
		return
	}
	if a.requireProject(w, r, core.PermissionProjectView, item.ProjectID) {
		writeJSON(w, 200, item)
	}
}
func (a *API) grantBackupObjects(ctx context.Context, b core.WorkloadBackup, storeID string, write, remove bool) (backupstore.Access, error) {
	s, err := a.backupObjectStore()
	if err != nil {
		return backupstore.Access{}, err
	}
	item, err := s.GetBackupObjectStore(ctx, storeID)
	if err != nil {
		return backupstore.Access{}, err
	}
	if item.ProjectID != b.ProjectID {
		return backupstore.Access{}, errors.New("offsite destination belongs to another project")
	}
	if b.Bytes > item.Config.MaxBytes {
		return backupstore.Access{}, errors.New("encrypted archive exceeds destination's accepted size limit")
	}
	credential, err := a.objectStoreCredentials(ctx, item)
	if err != nil {
		return backupstore.Access{}, err
	}
	return backupstore.Grant(item.Config, credential, item.ID, b.ProjectID, b.ID, write, remove, time.Now().UTC())
}
func (a *API) exportWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		problem(w, 422, "Idempotency key required", "Choose one stable key for this export operation.")
		return
	}
	var input struct {
		StoreID string `json:"storeId"`
	}
	if !decode(w, r, &input) {
		return
	}
	backups, _ := a.workloadBackupStore()
	b, err := backups.GetWorkloadBackup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Backup")
		return
	}
	if !a.requireProject(w, r, core.PermissionDeploymentRun, b.ProjectID) {
		return
	}
	if b.State != "ready" || b.LocalState == "retired" || b.Offsite != nil && b.Offsite.DeletedAt != nil || b.Checksum == "" || input.StoreID == "" {
		problem(w, 409, "Offsite export unavailable", "Choose a complete encrypted backup and assigned object store.")
		return
	}
	if b.Offsite != nil && b.Offsite.StoreID != input.StoreID {
		problem(w, 409, "Offsite identity changed", "A retained backup cannot move to a different object-store identity.")
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, b.ProjectID, "workload.backup.export", struct {
		BackupID string
		StoreID  string
	}{b.ID, input.StoreID}, "workload_backup", b.ID)
	if !proceed {
		return
	}
	id := ulid.Make().String()
	if receipt != nil {
		id = receipt.OperationID
	}
	request, err := a.decryptWorkloadBackup(b.ID, "accepted", b.EncryptedInput)
	if err != nil {
		a.internal(w, err)
		return
	}
	access, err := a.grantBackupObjects(r.Context(), b, input.StoreID, true, false)
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Offsite export was not accepted; inspect the destination and archive.")
		problem(w, 409, "Offsite destination unavailable", err.Error())
		return
	}
	request.Backup, request.OperationID, request.Action, request.OffsiteAccess = b, id, "export", &access
	op := newBackupOperation(id, b, "export")
	op.OffsiteStoreID = input.StoreID
	server, e := a.store.GetServer(r.Context(), b.ServerID)
	if e != nil || server.AgentNodeID != b.NodeID {
		problem(w, 409, "Source enrollment changed", "Inspect the original source target.")
		return
	}
	op.ExecutionServerID, op.ExecutionNodeID = server.ID, server.AgentNodeID
	if server.AgentNodeID != "" {
		data, ok := a.store.(workloadBackupEnrollmentReader)
		if !ok {
			a.internal(w, errors.New("target identity unavailable"))
			return
		}
		credential, e := data.GetEdgeCredential(r.Context(), server.AgentNodeID)
		if e != nil || credential.Revoked || credential.PublicKey == "" {
			problem(w, 409, "Source enrollment unavailable", "Inspect the original source target.")
			return
		}
		op.ExecutionGeneration = credential.Generation
		request.ExecutionNodeGeneration = credential.Generation
	}
	op.EncryptedInput, err = a.encryptWorkloadBackup(id, "operation", request)
	if err == nil {
		err = backups.CreateWorkloadBackupOperation(r.Context(), op, b.Revision)
	}
	if err != nil {
		a.failMutationAcceptance(r.Context(), 409, "Offsite export was not accepted; inspect the original operation.")
		problem(w, 409, "Offsite export blocked", "The backup or active operation changed.")
		return
	}
	core.RecordAcceptedOperation(r.Context(), op.ID)
	go a.executeWorkloadBackupOperation(op, false)
	a.writeBackupAcceptance(w, r, op, receipt)
}

func (a *API) backupExecutionGenerationMatches(ctx context.Context, op core.WorkloadBackupOperation) bool {
	if op.ExecutionNodeID == "" {
		return true
	}
	data, ok := a.store.(workloadBackupEnrollmentReader)
	if !ok {
		return false
	}
	credential, err := data.GetEdgeCredential(ctx, op.ExecutionNodeID)
	return err == nil && !credential.Revoked && credential.PublicKey != "" && credential.Generation == op.ExecutionGeneration
}

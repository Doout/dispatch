package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) retireLocalWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	a.mutateWorkloadBackup(w, r, "retire-local")
}
func (a *API) deleteOffsiteWorkloadBackup(w http.ResponseWriter, r *http.Request) {
	a.mutateWorkloadBackup(w, r, "delete-offsite")
}

func (a *API) workloadBackupRetirementReview(ctx context.Context, b core.WorkloadBackup, action string, out destructiveReview) (destructiveReview, error) {
	data, ok := a.store.(interface {
		WorkloadBackupRetirementAllowed(context.Context, core.WorkloadBackup, string) error
	})
	if !ok {
		return out, errors.New("backup retirement admission is unavailable")
	}
	if data.WorkloadBackupRetirementAllowed(ctx, b, action) != nil {
		out.BlockedReason = "The offsite copy must be independently verified, with no unresolved operations. Keep a usable recovery point and the policy's protected archives."
	}
	stores, err := a.backupObjectStore()
	if err != nil {
		return out, err
	}
	var destination core.BackupObjectStore
	if b.Offsite != nil {
		destination, err = stores.GetBackupObjectStore(ctx, b.Offsite.StoreID)
		if err != nil {
			return out, err
		}
		out.Resources = []string{"Archive: " + b.Offsite.ArchiveKey, "Manifest: " + b.Offsite.ManifestKey, "Archive SHA-256: " + b.Checksum, "Manifest SHA-256: " + b.Offsite.ManifestChecksum, "Object store: " + destination.ID, "Controller recovery metadata and encryption key remain"}
	}
	if action == "retire-local" {
		out.Summary = "Verify and authenticate the encrypted offsite archive and manifest again, then remove only the original local archive bytes. Confirmed retirement releases the source target's backup protection."
		out.StoragePolicy = "retain-offsite"
	} else {
		out.Summary = "Conditionally delete only these owned encrypted offsite objects. Preserve a verified local copy or another verified recovery point. Controller metadata and history remain."
		out.StoragePolicy = "destroy-offsite"
		if !destination.Config.ConditionalDelete {
			out.BlockedReason = "The object store must explicitly support conditional unversioned deletion. Versioned objects remain protected."
		}
	}
	var policy any
	if b.CapturePolicyID != "" {
		policies, err := a.backupPolicyReader()
		if err != nil {
			return out, err
		}
		policy, err = policies.GetWorkloadBackupPolicy(ctx, b.CapturePolicyID)
		if err != nil {
			return out, err
		}
	}
	backups, _ := a.workloadBackupStore()
	alternatives, err := backups.ListWorkloadBackups(ctx, b.ProjectID)
	if err != nil {
		return out, err
	}
	// Bind recovery choices and policy state to the confirmation. Admission locks
	// and rechecks the backup, policy and remaining recovery points transactionally.
	relevant := []core.WorkloadBackup{}
	for _, other := range alternatives {
		if other.SourceRunID == b.SourceRunID {
			relevant = append(relevant, other)
		}
	}
	out.Version = mutationHash(struct {
		Backup         core.WorkloadBackup
		Store          core.BackupObjectStore
		Policy         any
		RecoveryPoints []core.WorkloadBackup
		Action         string
	}{b, destination, policy, relevant, action})
	return out, nil
}

func (a *API) setBackupObjectStoreConditionalDelete(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled         *bool  `json:"enabled"`
		ExpectedEnabled *bool  `json:"expectedEnabled"`
		ConfirmName     string `json:"confirmName"`
	}
	if !decode(w, r, &input) {
		return
	}
	data, err := a.backupObjectStore()
	if err != nil {
		a.internal(w, err)
		return
	}
	item, err := data.GetBackupObjectStore(r.Context(), chi.URLParam(r, "storeId"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Object store")
		return
	}
	if input.Enabled == nil || input.ExpectedEnabled == nil || input.ConfirmName != item.Name {
		problem(w, 422, "Confirmation required", "Supply explicit enabled and expectedEnabled values and the exact registered store name.")
		return
	}
	capabilities, ok := a.store.(interface {
		SetBackupObjectStoreConditionalDelete(context.Context, string, bool, bool) error
	})
	if !ok {
		a.internal(w, errors.New("conditional object capability storage unavailable"))
		return
	}
	if err = capabilities.SetBackupObjectStoreConditionalDelete(r.Context(), item.ID, *input.ExpectedEnabled, *input.Enabled); err != nil {
		problem(w, 409, "Object store changed", "Inspect the current store and reconcile its active operations before changing conditional deletion support.")
		return
	}
	item, err = data.GetBackupObjectStore(r.Context(), item.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, item)
}

func intactVerifiedOffsite(b core.WorkloadBackup) bool {
	return b.CleanupState == "complete" && b.Offsite != nil && b.Offsite.DeletedAt == nil && b.Offsite.VerifiedAt != nil && b.Offsite.VerificationState == "verified" && !b.Offsite.ConfirmedAt.IsZero() && b.Offsite.ManifestChecksum != "" && b.Checksum != ""
}

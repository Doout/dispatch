package remoteruntime

import (
	"context"
	"errors"

	"github.com/doout/dispatch/internal/runtimecontract"
)

// BackupAuthorityBinding reads the original operation identity from the durable
// authenticated request. Reconciliation jobs intentionally have independent job IDs.
// Credential material stays inside the broker and is not returned by this lookup.
type BackupAuthorityBinding struct {
	SourceRunID      string
	TargetRunID      string
	SourceResourceID string
	OffsiteStoreID   string
	OperationID      string
	BackupID         string
	ProjectID        string
	ServerID         string
	NodeID           string
	NodeGeneration   int64
}

func (b *Broker) BackupAuthority(ctx context.Context, id string) (*BackupAuthorityBinding, error) {
	job, err := b.Store.GetRuntimeJob(ctx, id)
	if err != nil {
		return nil, err
	}
	switch runtimecontract.Operation(job.Operation) {
	case WorkloadBackup, WorkloadBackupInspect, WorkloadBackupOffsite, WorkloadBackupOffsiteInspect, runtimecontract.Operation("workload_backup_retire"), runtimecontract.Operation("workload_backup_retire_inspect"):
	default:
		return nil, nil
	}
	request, err := b.request(job)
	if err != nil {
		return nil, err
	}
	if request.WorkloadBackup == nil || request.WorkloadBackup.OperationID == "" {
		return nil, errors.New("typed backup runtime request lost its original operation identity")
	}
	binding := &BackupAuthorityBinding{SourceRunID: request.WorkloadBackup.Source.Run.ID, SourceResourceID: request.WorkloadBackup.Backup.SourceResourceID, OperationID: request.WorkloadBackup.OperationID, BackupID: request.WorkloadBackup.Backup.ID, ProjectID: job.ProjectID, ServerID: job.ServerID, NodeID: job.NodeID, NodeGeneration: job.NodeGeneration}
	if request.WorkloadBackup.Destination != nil {
		binding.TargetRunID = request.WorkloadBackup.Destination.Run.ID
	}
	if request.WorkloadBackup.OffsiteAccess != nil {
		binding.OffsiteStoreID = request.WorkloadBackup.OffsiteAccess.StoreID
	}
	return binding, nil
}

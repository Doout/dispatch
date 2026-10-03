package remoteruntime

import (
	"context"

	"github.com/doout/dispatch/internal/runtimecontract"
)

// BackupCacheCleanupCapability identifies recovery jobs that must remove their
// downloaded archive. Older offsite engines leave that cache on the target.
func (r Request) BackupCacheCleanupCapability() runtimecontract.Operation {
	b := r.WorkloadBackup
	if b == nil || b.OffsiteAccess == nil {
		return ""
	}
	action := b.Action
	if action == "reconcile" {
		action = b.RecoveryAction
	}
	if (action != "verify" && action != "restore") || b.Backup.LocalState != "retired" && b.Backup.ServerID == r.Server.ID {
		return ""
	}
	if b.Action == "reconcile" {
		return WorkloadBackupRetireInspect
	}
	return WorkloadBackupRetire
}

// Resolve renewal requirements from the authenticated durable request, rather
// than mutable inventory or a caller's heartbeat body. No inputs are returned.
func (b *Broker) BackupCacheCleanupCapability(ctx context.Context, id string) (runtimecontract.Operation, error) {
	job, err := b.Store.GetRuntimeJob(ctx, id)
	if err != nil {
		return "", err
	}
	r, err := b.request(job)
	if err != nil {
		return "", err
	}
	return r.BackupCacheCleanupCapability(), nil
}

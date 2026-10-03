package remoteruntime

import (
	"errors"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

const WorkloadBackup runtimecontract.Operation = "workload_backup"
const WorkloadBackupInspect runtimecontract.Operation = "workload_backup_inspect"
const WorkloadBackupOffsite runtimecontract.Operation = "workload_backup_offsite"
const WorkloadBackupOffsiteInspect runtimecontract.Operation = "workload_backup_offsite_inspect"
const WorkloadBackupRetire runtimecontract.Operation = "workload_backup_retire"
const WorkloadBackupRetireInspect runtimecontract.Operation = "workload_backup_retire_inspect"

func IsWorkloadBackupOperation(op runtimecontract.Operation) bool {
	switch op {
	case WorkloadBackup, WorkloadBackupInspect, WorkloadBackupOffsite, WorkloadBackupOffsiteInspect, WorkloadBackupRetire, WorkloadBackupRetireInspect:
		return true
	default:
		return false
	}
}

func NewWorkloadBackupRequest(input core.WorkloadBackupRequest, server core.Server) Request {
	subject := input.Source
	if input.Destination != nil && (input.Action == "restore" || input.Action == "verify" || input.Action == "reconcile" && (input.RecoveryAction == "restore" || input.RecoveryAction == "verify")) {
		subject = *input.Destination
	}
	r := NewServiceRequest(subject, core.DockerServiceProvision{ServerRef: server.ID}, server)
	r.WorkloadBackup = &input
	r.Operation = WorkloadBackup
	if input.Action == "inspect" || input.Action == "reconcile" {
		r.Operation = WorkloadBackupInspect
	}
	if input.OffsiteAccess != nil {
		if r.Operation == WorkloadBackupInspect {
			r.Operation = WorkloadBackupOffsiteInspect
		} else {
			r.Operation = WorkloadBackupOffsite
		}
	}
	if input.Action == "retire-local" {
		r.Operation = WorkloadBackupRetire
	}
	if input.Action == "reconcile" && input.RecoveryAction == "retire-local" {
		r.Operation = WorkloadBackupRetireInspect
	}
	return r
}
func (r Request) validateWorkloadBackup() error {
	if err := r.validateService(); err != nil {
		return err
	}
	b := r.WorkloadBackup
	if b != nil && (b.OffsiteAccess != nil) != (r.Operation == WorkloadBackupOffsite || r.Operation == WorkloadBackupOffsiteInspect || r.Operation == WorkloadBackupRetire || r.Operation == WorkloadBackupRetireInspect) {
		return errors.New("offsite operation requires explicit runtime capability")
	}
	if b == nil || !identityPattern.MatchString(b.Backup.ID) || !identityPattern.MatchString(b.OperationID) || b.Backup.ProjectID != r.Application.ProjectID || (b.Backup.ServerID != r.Server.ID && !offsiteRuntimeRecovery(b, r.Server)) || b.Source.Run.ProjectID != r.Application.ProjectID || b.Source.Run.ID != b.Backup.SourceRunID || (b.Backup.ServerID == r.Server.ID && b.Backup.NodeID != r.Server.AgentNodeID) || b.Backup.ArtifactID != b.Backup.ID || b.Storage.ProjectID != r.Application.ProjectID || b.Storage.ServerID != b.Backup.ServerID || len(b.Key) != 64 {
		return errors.New("backup ownership or encryption inputs do not match runtime target")
	}
	if (r.Operation == WorkloadBackupInspect || r.Operation == WorkloadBackupOffsiteInspect || r.Operation == WorkloadBackupRetireInspect) != (b.Action == "inspect" || b.Action == "reconcile") {
		return errors.New("backup inspection cannot contain a data mutation")
	}
	if (b.Action == "retire-local" || b.Action == "reconcile" && b.RecoveryAction == "retire-local") != (r.Operation == WorkloadBackupRetire || r.Operation == WorkloadBackupRetireInspect) {
		return errors.New("local archive retirement requires explicit runtime capability")
	}
	return nil
}
func (r Request) ValidateWorkloadBackupResult(result Result) error {
	if r.WorkloadBackup == nil {
		if result.WorkloadBackup != nil {
			return errors.New("unexpected workload backup evidence")
		}
		return nil
	}
	item := result.WorkloadBackup
	if item == nil {
		if result.State == "succeeded" {
			return errors.New("backup result evidence is missing")
		}
		return nil
	}
	if item.BackupID != r.WorkloadBackup.Backup.ID || item.ProjectID != r.Application.ProjectID || item.OperationID != r.WorkloadBackup.OperationID || item.ArtifactID != r.WorkloadBackup.Backup.ID || item.Bytes < 0 || len(item.Message) > 2048 {
		return errors.New("backup evidence ownership is invalid")
	}
	if item.State == "exported" {
		a := r.WorkloadBackup.OffsiteAccess
		if item.Offsite == nil || a == nil || item.Offsite.StoreID != a.StoreID || item.Offsite.ArchiveKey != a.Archive.Key || item.Offsite.ManifestKey != a.Manifest.Key || len(item.Offsite.ManifestChecksum) != 64 || item.Offsite.ImageReference == "" {
			return errors.New("offsite export evidence ownership is invalid")
		}
	}
	switch item.State {
	case "ready", "verified", "restored", "deleted", "failed", "unknown", "unresolved", "exported", "local-retired":
	default:
		return errors.New("invalid backup outcome")
	}
	action := r.WorkloadBackup.Action
	if action == "reconcile" {
		action = r.WorkloadBackup.RecoveryAction
	}
	expected := map[string]string{"backup": "ready", "inspect": "ready", "verify": "verified", "restore": "restored", "delete": "deleted", "export": "exported", "retire-local": "local-retired"}[action]
	if item.State != "failed" && item.State != "unknown" && item.State != "unresolved" && item.State != expected {
		return errors.New("backup evidence does not match the accepted action")
	}
	if item.State == expected && item.CleanupState != "complete" {
		return errors.New("completed backup evidence requires confirmed cleanup")
	}
	switch item.CleanupState {
	case "complete", "pending", "failed":
	default:
		return errors.New("invalid backup cleanup outcome")
	}
	return nil
}

func offsiteRuntimeRecovery(b *core.WorkloadBackupRequest, s core.Server) bool {
	action := b.Action
	if action == "reconcile" {
		action = b.RecoveryAction
	}
	return (action == "restore" || action == "verify") && b.Backup.Offsite != nil && b.OffsiteAccess != nil && b.OffsiteAccess.StoreID == b.Backup.Offsite.StoreID && b.Destination != nil && b.Destination.Run.Target != nil && b.Destination.Run.Target.ServerID == s.ID && b.Destination.Run.ProjectID == b.Backup.ProjectID
}

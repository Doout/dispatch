package remoteruntime

import (
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

const WorkloadBackup runtimecontract.Operation = "workload_backup"
const WorkloadBackupInspect runtimecontract.Operation = "workload_backup_inspect"

func NewWorkloadBackupRequest(input core.WorkloadBackupRequest, server core.Server) Request {
	subject := input.Source
	if input.Destination != nil && (input.Action == "restore" || input.Action == "reconcile" && input.RecoveryAction == "restore") {
		subject = *input.Destination
	}
	r := NewServiceRequest(subject, core.DockerServiceProvision{ServerRef: server.ID}, server)
	r.WorkloadBackup = &input
	r.Operation = WorkloadBackup
	if input.Action == "inspect" || input.Action == "reconcile" {
		r.Operation = WorkloadBackupInspect
	}
	return r
}
func (r Request) validateWorkloadBackup() error {
	if err := r.validateService(); err != nil {
		return err
	}
	b := r.WorkloadBackup
	if b == nil || !identityPattern.MatchString(b.Backup.ID) || !identityPattern.MatchString(b.OperationID) || b.Backup.ProjectID != r.Application.ProjectID || b.Backup.ServerID != r.Server.ID || b.Source.Run.ProjectID != r.Application.ProjectID || b.Source.Run.ID != b.Backup.SourceRunID || b.Backup.NodeID != r.Server.AgentNodeID || b.Backup.ArtifactID != b.Backup.ID || b.Storage.ProjectID != r.Application.ProjectID || b.Storage.ServerID != r.Server.ID || len(b.Key) != 64 {
		return errors.New("backup ownership or encryption inputs do not match runtime target")
	}
	if (r.Operation == WorkloadBackupInspect) != (b.Action == "inspect" || b.Action == "reconcile") {
		return errors.New("backup inspection cannot contain a data mutation")
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
	switch item.State {
	case "ready", "verified", "restored", "deleted", "failed", "unknown", "unresolved":
	default:
		return errors.New("invalid backup outcome")
	}
	action := r.WorkloadBackup.Action
	if action == "reconcile" {
		action = r.WorkloadBackup.RecoveryAction
	}
	expected := map[string]string{"backup": "ready", "inspect": "ready", "verify": "verified", "restore": "restored", "delete": "deleted"}[action]
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

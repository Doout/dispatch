package remoteruntime

import (
	"github.com/doout/dispatch/internal/core"
	"strings"
	"testing"
)

func TestWorkloadBackupProtocolOwnershipAndTypedInspection(t *testing.T) {
	server := core.Server{ID: "server", AgentNodeID: "node", Runtime: "docker"}
	source := core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project", ServiceName: "db", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: "server"}}, ServiceType: "postgresql"}
	request := NewWorkloadBackupRequest(core.WorkloadBackupRequest{OperationID: "operation", Action: "backup", Backup: core.WorkloadBackup{ID: "backup", ArtifactID: "backup", ProjectID: "project", ServerID: "server", NodeID: "node", SourceRunID: "run"}, Source: source, Storage: core.StorageResource{ProjectID: "project", ServerID: "server"}, Key: strings.Repeat("a", 64)}, server)
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	request.Operation = WorkloadBackupInspect
	if err := request.Validate(); err == nil {
		t.Fatal("inspection accepted data mutation")
	}
	request.Operation = WorkloadBackup
	request.WorkloadBackup.Backup.ProjectID = "other"
	if err := request.Validate(); err == nil {
		t.Fatal("cross-project archive accepted")
	}
	request.WorkloadBackup.Backup.ProjectID = "project"
	result := Result{State: "succeeded", WorkloadBackup: &core.WorkloadBackupResult{BackupID: "backup", ArtifactID: "backup", ProjectID: "project", OperationID: "operation", State: "ready", CleanupState: "complete"}}
	if err := request.ValidateWorkloadBackupResult(result); err != nil {
		t.Fatal(err)
	}
	result.WorkloadBackup.State = "restored"
	if err := request.ValidateWorkloadBackupResult(result); err == nil {
		t.Fatal("result from a different action accepted")
	}
	result.WorkloadBackup.State = "ready"
	result.WorkloadBackup.BackupID = "another"
	if err := request.ValidateWorkloadBackupResult(result); err == nil {
		t.Fatal("foreign backup evidence accepted")
	}
}

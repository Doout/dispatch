package remoteruntime

import (
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
)

func TestWorkloadBackupRetirementExplicitMutationAndInspectionCapabilities(t *testing.T) {
	_, base := brokerFixture(t)
	source := core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: "server"}}, ServiceType: "postgresql"}
	input := core.WorkloadBackupRequest{OperationID: "retire-original", Action: "retire-local", Backup: core.WorkloadBackup{ID: "backup", ArtifactID: "backup", ProjectID: "project", ServerID: "server", NodeID: "node", SourceRunID: "run"}, Source: source, Storage: core.StorageResource{ProjectID: "project", ServerID: "server"}, Key: strings.Repeat("a", 64), OffsiteAccess: &backupstore.Access{StoreID: "store", MaxBytes: 100}}
	request := NewWorkloadBackupRequest(input, base.Server)
	if request.Operation != WorkloadBackupRetire || request.Validate() != nil {
		t.Fatal("retirement capability was not required", request.Validate())
	}
	request.Operation = WorkloadBackupOffsite
	if request.Validate() == nil {
		t.Fatal("old export capability accepted retirement")
	}
	request = NewWorkloadBackupRequest(input, base.Server)
	result := Result{State: "succeeded", WorkloadBackup: &core.WorkloadBackupResult{BackupID: "backup", ProjectID: "project", OperationID: "retire-original", ArtifactID: "backup", State: "local-retired", CleanupState: "complete"}}
	if err := request.ValidateWorkloadBackupResult(result); err != nil {
		t.Fatal(err)
	}
	result.WorkloadBackup.State = "deleted"
	if request.ValidateWorkloadBackupResult(result) == nil {
		t.Fatal("wholearchive deletion accepted as local retirement")
	}
	result.WorkloadBackup.State = "local-retired"
	result.WorkloadBackup.CleanupState = "pending"
	if request.ValidateWorkloadBackupResult(result) == nil {
		t.Fatal("unconfirmed local deletion accepted")
	}
	input.Action, input.RecoveryAction = "reconcile", "retire-local"
	request = NewWorkloadBackupRequest(input, base.Server)
	if request.Operation != WorkloadBackupRetireInspect || request.Validate() != nil {
		t.Fatal("recovery did not require retirement inspection", request.Validate())
	}
	request.WorkloadBackup.Action = "retire-local"
	if request.Validate() == nil {
		t.Fatal("retirement mutation accepted as inspection")
	}
}

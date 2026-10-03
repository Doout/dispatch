package remoteruntime

import (
	"context"
	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"strings"
	"testing"
	"time"
)

func TestWorkloadBackupOffsiteExplicitCapabilityAndFrozenEnrollment(t *testing.T) {
	broker, base := brokerFixture(t)
	data := broker.Store.(*store.SQLStore)
	ctx := context.Background()
	credential, err := data.GetEdgeCredential(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	source := core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: "server"}}, ServiceType: "postgresql"}
	input := core.WorkloadBackupRequest{OperationID: "operation", Action: "export", ExecutionNodeGeneration: credential.Generation, Backup: core.WorkloadBackup{ID: "backup", ArtifactID: "backup", ProjectID: "project", ServerID: "server", NodeID: "node", SourceRunID: "run"}, Source: source, Storage: core.StorageResource{ProjectID: "project", ServerID: "server"}, Key: strings.Repeat("a", 64), OffsiteAccess: &backupstore.Access{StoreID: "store", MaxBytes: 1 << 30, Archive: backupstore.ObjectAccess{Key: "archive"}, Manifest: backupstore.ObjectAccess{Key: "manifest"}}}
	request := NewWorkloadBackupRequest(input, base.Server)
	if request.Operation != WorkloadBackupOffsite || request.Validate() != nil {
		t.Fatal("export did not require its implemented offsite capability")
	}
	request.Operation = WorkloadBackup
	if request.Validate() == nil {
		t.Fatal("generic backup capability accepted an offsite request")
	}
	input.Action, input.RecoveryAction = "reconcile", "export"
	request = NewWorkloadBackupRequest(input, base.Server)
	if request.Operation != WorkloadBackupOffsiteInspect || request.Validate() != nil {
		t.Fatal("recovery was not isolated to an inspection capability")
	}
	now := time.Now().UTC()
	if err = data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "replacement", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = data.EnrollEdgeCredential(ctx, "node", "replacement", "new-public-key", "new-session", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = broker.Submit(ctx, "frozen-offsite-operation", request); err == nil || !strings.Contains(err.Error(), "enrollment changed") {
		t.Fatal("accepted generation was silently transferred", err)
	}
	if _, err = data.GetRuntimeJob(ctx, "frozen-offsite-operation"); err != store.ErrNotFound {
		t.Fatal("replacement enrollment received a runtime job", err)
	}
	input.ExecutionNodeGeneration = 0
	request = NewWorkloadBackupRequest(input, base.Server)
	if _, err = broker.Submit(ctx, "unfrozen-offsite-operation", request); err == nil {
		t.Fatal("offsite request without accepted enrollment was dispatched")
	}
}

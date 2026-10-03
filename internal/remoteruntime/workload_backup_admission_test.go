package remoteruntime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestOffsiteRuntimeAdmissionLeaseAndUnknownInspection(t *testing.T) {
	broker, base := brokerFixture(t)
	data := broker.Store.(*store.SQLStore)
	ctx := context.Background()
	now := time.Now().UTC()
	run := core.ServiceProvisionRun{ID: "offsite-source-run", TemplateID: "approved-template", ProjectID: base.Application.ProjectID, ServiceName: "database", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: base.Server.ID}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: run.ProjectID, ServiceID: "database-service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Policy: "retain", Revision: 1, OperationID: run.ID, EncryptedRequest: "fixture-encrypted-service", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateServiceResource(ctx, run, resource); err != nil {
		t.Fatal(err)
	}
	credential, err := data.GetEdgeCredential(ctx, base.Server.AgentNodeID)
	if err != nil {
		t.Fatal(err)
	}
	access, err := backupstore.Grant(backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "backups", Region: "us-east-1", Prefix: "dispatch", MaxBytes: 1 << 20}, backupstore.Credentials{AccessKeyID: "FIXTUREACCESS", SecretAccessKey: "fixture-signing-secret"}, "store", run.ProjectID, "backup", true, false, now)
	if err != nil {
		t.Fatal(err)
	}
	input := core.WorkloadBackupRequest{OperationID: "accepted-export", Action: "export", ExecutionNodeGeneration: credential.Generation, Backup: core.WorkloadBackup{ID: "backup", ArtifactID: "backup", ProjectID: run.ProjectID, ServerID: base.Server.ID, NodeID: base.Server.AgentNodeID, SourceRunID: run.ID}, Source: core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Password: "fixture-database-password"}, Storage: core.StorageResource{ProjectID: run.ProjectID, ServerID: base.Server.ID}, Key: strings.Repeat("a", 64), OffsiteAccess: &access}
	request := NewWorkloadBackupRequest(input, base.Server)
	original, err := broker.Submit(ctx, "backup-"+input.OperationID, request)
	if err != nil {
		t.Fatal("service-owned offsite mutation was not admitted", err)
	}
	lease, err := broker.Lease(ctx, base.Server.AgentNodeID)
	if err != nil || lease == nil || lease.ID != original.ID || lease.Request.Operation != WorkloadBackupOffsite || lease.Request.WorkloadBackup.OffsiteAccess.Archive.Put != access.Archive.Put {
		t.Fatal("scoped offsite payload did not survive encryption and leasing", err)
	}
	if err = data.FenceRuntimeJob(ctx, base.Server.AgentNodeID, original.ID, lease.LeaseToken, time.Now()); err != nil {
		t.Fatal(err)
	}
	input.Action, input.RecoveryAction = "reconcile", "export"
	inspection := NewWorkloadBackupRequest(input, base.Server)
	if _, err = broker.Submit(ctx, "offsite-inspection", inspection); err != nil {
		t.Fatal("unknown mutation blocked its read-only offsite inspection", err)
	}
	lease, err = broker.Lease(ctx, base.Server.AgentNodeID)
	if err != nil || lease == nil || lease.ID != "offsite-inspection" || lease.Request.Operation != WorkloadBackupOffsiteInspect {
		t.Fatal("offsite inspection was not leased to its owned service", err)
	}
	evidence := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: run.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, State: "exported", CleanupState: "complete", Offsite: &core.BackupOffsiteArtifact{StoreID: access.StoreID, ArchiveKey: access.Archive.Key, ManifestKey: access.Manifest.Key, ManifestChecksum: strings.Repeat("b", 64), ImageReference: "postgres@sha256:" + strings.Repeat("c", 64)}}
	if err = broker.Complete(ctx, base.Server.AgentNodeID, lease.ID, Completion{LeaseToken: lease.LeaseToken, Result: Result{State: "succeeded", WorkloadBackup: &evidence}}); err != nil {
		t.Fatal("offsite inspection completion was not accepted", err)
	}
	result, err := broker.Wait(ctx, lease.ID, nil)
	if err != nil || result.WorkloadBackup == nil || result.WorkloadBackup.Offsite.ManifestChecksum != evidence.Offsite.ManifestChecksum {
		t.Fatal("offsite completion evidence was lost", err)
	}
}

package store

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkloadBackupSQLiteAdmissionAndRestart(t *testing.T) {
	testWorkloadBackupStore(t, filepath.Join(t.TempDir(), "backups.db"))
}
func TestWorkloadBackupPostgresAdmissionAndRestart(t *testing.T) {
	url := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testWorkloadBackupStore(t, url)
}
func testWorkloadBackupStore(t *testing.T, url string) {
	ctx := context.Background()
	s := mutationStore(t, url)
	prefix := ulid.Make().String()
	now := time.Now().UTC()
	project := core.Project{ID: prefix + "p", Name: prefix, CreatedAt: now}
	server := core.Server{ID: prefix + "s", Name: prefix, Runtime: "docker", Address: "local", CreatedAt: now}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	run := core.ServiceProvisionRun{ID: prefix + "r", TemplateID: prefix + "template", ProjectID: project.ID, ServiceName: "database", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: prefix}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: prefix + "service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Revision: 1, OperationID: run.ID, Policy: "retain", EncryptedRequest: "encrypted accepted source", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateServiceResource(ctx, run, resource); err != nil {
		t.Fatal(err)
	}
	resource, err := s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "initial", now, "")
	if err != nil {
		t.Fatal(err)
	}
	resource.State, resource.ResourceID = "ready", "owned-container"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	connection := core.Service{ID: resource.ServiceID, ProjectID: project.ID, Name: run.ServiceName, Type: "postgresql", ProvisionRunID: run.ID, ProvisionTarget: run.Target, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = s.SaveServiceResource(ctx, resource, run, &connection); err != nil {
		t.Fatal(err)
	}
	backup := core.WorkloadBackup{ID: prefix + "backup", ProjectID: project.ID, ServerID: server.ID, SourceRunID: run.ID, State: "creating", Revision: 1, Policy: "retain", Location: "target-local", EncryptedInput: "wrapped-key", CreatedAt: now, UpdatedAt: now}
	op := core.WorkloadBackupOperation{ID: prefix + "op", BackupID: backup.ID, ProjectID: project.ID, Action: "backup", State: "running", Revision: 1, LeaseToken: "original", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "accepted-job", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateWorkloadBackup(ctx, backup, op); err != nil {
		t.Fatal(err)
	}
	backup.State, backup.Checksum = "ready", strings.Repeat("a", 64)
	op.State = "succeeded"
	if err = s.CompleteWorkloadBackupOperation(ctx, backup, op); err != nil {
		t.Fatal(err)
	}
	backup, _ = s.GetWorkloadBackup(ctx, backup.ID)
	restore := core.WorkloadBackupOperation{ID: prefix + "restore", BackupID: backup.ID, ProjectID: project.ID, Action: "restore", State: "running", TargetRunID: run.ID, TargetResourceID: resource.ResourceID, Revision: 1, LeaseToken: "old-process", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted-destination", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateWorkloadBackupOperation(ctx, restore, backup.Revision); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: prefix + "app", ProjectID: project.ID, ServerID: server.ID, Name: "Consumer", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	if err = s.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, []core.ServiceBinding{{Alias: "db", ServiceRef: connection.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}); err == nil {
		t.Fatal("new consumer bypassed pending restore")
	}
	resource, _ = s.GetServiceResource(ctx, run.ID)
	if _, err = s.ClaimServiceResource(ctx, run.ID, resource.Revision, "delete", "deleting", "token", now, resource.ResourceID); err == nil {
		t.Fatal("resource deletion bypassed active data restore")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.ClaimWorkloadBackupRecovery(ctx, restore.ID, now, "too-early"); !errors.Is(err, ErrWorkloadBackupChanged) {
		t.Fatal("recovery ignored original lease", err)
	}
	claim, err := s.ClaimWorkloadBackupRecovery(ctx, restore.ID, now.Add(32*time.Minute), "new-process")
	if err != nil {
		t.Fatal(err)
	}
	restore.State = "succeeded"
	if err = s.CompleteWorkloadBackupOperation(ctx, backup, restore); err == nil {
		t.Fatal("stale process changed recovered operation")
	}
	claim.State = "unresolved"
	if err = s.CompleteWorkloadBackupOperation(ctx, backup, claim); err != nil {
		t.Fatal(err)
	}
	backup, _ = s.GetWorkloadBackup(ctx, backup.ID)
	if backup.EncryptedInput != "wrapped-key" {
		t.Fatal("restart discarded retained recovery key")
	}
	// A fresh reviewed operation may proceed after explicit reconciliation. The old ID stays unresolved.
	restore.ID, restore.LeaseToken, restore.State = prefix+"retry", "reviewed", "running"
	if err = s.CreateWorkloadBackupOperation(ctx, restore, backup.Revision); err != nil {
		t.Fatal(err)
	}
	restore.State = "succeeded"
	if err = s.CompleteWorkloadBackupOperation(ctx, backup, restore); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, s.q(`DELETE FROM servers WHERE id=?`), server.ID)
	if err == nil || !strings.Contains(err.Error(), "backup") {
		t.Fatal("retained archives did not protect server", err)
	}
	// Remote recovery requires fresh evidence from the current enrolled target,
	// after the original lease, and only acknowledges the accepted operation ID.
	node := prefix + "node"
	if err = s.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: node, Name: node, Driver: "edge", State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	execSQL := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(ctx, s.q(query), args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL(`INSERT INTO edge_node_credentials(network_id,generation,enrollment_expires_at,public_key,session_expires_at,updated_at) VALUES(?,1,?,'fixture-key',?,?)`, node, stamp(now), stamp(now.Add(time.Hour)), stamp(now))
	execSQL(`UPDATE servers SET agent_node_id=? WHERE id=?`, node, server.ID)
	job := core.RuntimeJob{ID: "backup-" + restore.ID, ServerID: server.ID, NodeID: node, NodeGeneration: 1, ProjectID: project.ID, AppID: "service-" + run.ID, ServiceRunID: run.ID, Operation: "workload_backup", RequestDigest: "accepted-digest", EncryptedRequest: "encrypted-request", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = s.CreateRuntimeJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	execSQL(`UPDATE runtime_jobs SET state='unknown',lease_until=?,updated_at=? WHERE id=?`, stamp(now.Add(time.Minute)), stamp(now), job.ID)
	inspection := job
	inspection.ID, inspection.Operation, inspection.CreatedAt = prefix+"inspection", "workload_backup_inspect", now.Add(2*time.Minute)
	if err = s.CreateRuntimeJob(ctx, inspection); err != nil {
		t.Fatal(err)
	}
	execSQL(`UPDATE runtime_jobs SET state='succeeded' WHERE id=?`, inspection.ID)
	if err = s.ReconcileWorkloadBackupRuntime(ctx, run.ID, restore.ID, inspection.ID, now); !errors.Is(err, ErrRuntimeJobConflict) {
		t.Fatal("remote recovery ignored old lease", err)
	}
	if err = s.ReconcileWorkloadBackupRuntime(ctx, run.ID, "different-operation", inspection.ID, now.Add(3*time.Minute)); !errors.Is(err, ErrRuntimeJobConflict) {
		t.Fatal("remote recovery acknowledged another operation", err)
	}
	execSQL(`UPDATE runtime_jobs SET created_at=? WHERE id=?`, stamp(now.Add(-time.Second)), inspection.ID)
	if err = s.ReconcileWorkloadBackupRuntime(ctx, run.ID, restore.ID, inspection.ID, now.Add(3*time.Minute)); !errors.Is(err, ErrRuntimeJobConflict) {
		t.Fatal("remote recovery accepted stale inspection", err)
	}
	execSQL(`UPDATE runtime_jobs SET created_at=? WHERE id=?`, stamp(inspection.CreatedAt), inspection.ID)
	execSQL(`UPDATE edge_node_credentials SET revoked=TRUE WHERE network_id=?`, node)
	if err = s.ReconcileWorkloadBackupRuntime(ctx, run.ID, restore.ID, inspection.ID, now.Add(3*time.Minute)); !errors.Is(err, ErrRuntimeJobConflict) {
		t.Fatal("remote recovery accepted revoked target evidence", err)
	}
	execSQL(`UPDATE edge_node_credentials SET revoked=FALSE WHERE network_id=?`, node)
	if err = s.ReconcileWorkloadBackupRuntime(ctx, run.ID, restore.ID, inspection.ID, now.Add(3*time.Minute)); err != nil {
		t.Fatal("remote recovery rejected fresh owned evidence", err)
	}
	savedJob, err := s.GetRuntimeJob(ctx, job.ID)
	if err != nil || savedJob.State != "acknowledged" {
		t.Fatal("remote operation remained blocked", savedJob.State, err)
	}
}

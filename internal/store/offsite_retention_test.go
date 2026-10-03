package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestOffsiteRetentionSQLiteRejectsProtectedArchive(t *testing.T) {
	testOffsiteRetentionGuard(t, filepath.Join(t.TempDir(), "offsite-retention.db"))
}

func TestOffsiteRetentionPostgresRejectsProtectedArchive(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testOffsiteRetentionGuard(t, dsn)
}

func testOffsiteRetentionGuard(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	s := mutationStore(t, dsn)
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	prefix := ulid.Make().String()
	project := core.Project{ID: prefix + "project", Name: "Offsite retention", CreatedAt: now}
	server := core.Server{ID: prefix + "server", Name: "Database target", Runtime: "docker", Address: "local", CreatedAt: now}
	check(s.CreateProject(ctx, project))
	check(s.CreateServer(ctx, server))
	run := core.ServiceProvisionRun{ID: prefix + "run", TemplateID: prefix + "template", ProjectID: project.ID, ServiceName: "pg", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: prefix}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: prefix + "service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Revision: 1, OperationID: run.ID, Policy: "retain", EncryptedRequest: "encrypted-fixture", CreatedAt: now, UpdatedAt: now}
	check(s.CreateServiceResource(ctx, run, resource))
	resource, err := s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "fixture", now, "")
	check(err)
	resource.State, resource.ResourceID = "ready", "owned-container"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	check(s.SaveServiceResource(ctx, resource, run, nil))
	secret := core.Secret{ID: prefix + "credential", Name: "Object credential", Type: core.SecretTypeJSON, EncryptedValue: "encrypted-fixture", CreatedAt: now, UpdatedAt: now}
	check(s.CreateSecret(ctx, secret))
	objectStore := core.BackupObjectStore{ID: prefix + "object-store", ProjectID: project.ID, Name: "Retained exports", CredentialSecretID: secret.ID, Config: backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "backups", Region: "us-east-1", Prefix: "retained", MaxBytes: 1024}, CreatedAt: now}
	check(s.CreateBackupObjectStore(ctx, objectStore))
	policy := core.WorkloadBackupPolicy{ID: prefix + "policy", ProjectID: project.ID, SourceRunID: run.ID, SourceResourceID: resource.ResourceID, ServerID: server.ID, Enabled: true, Revision: 1, IntervalHours: 1, KeepLast: 1, EncryptedInput: "encrypted-policy", CreatedAt: now, UpdatedAt: now}
	check(s.CreateWorkloadBackupPolicy(ctx, policy))
	var oldest core.WorkloadBackup
	for i := range 2 {
		policy.NextCaptureAt = now.Add(-time.Duration(2-i) * time.Minute)
		check(s.UpdateWorkloadBackupPolicy(ctx, policy, policy.Revision))
		policy, err = s.GetWorkloadBackupPolicy(ctx, policy.ID)
		check(err)
		slot, _, _ := policy.DueCapture(now)
		id := ulid.Make().String()
		created := now.Add(time.Duration(i) * time.Second)
		b := core.WorkloadBackup{ID: id, CapturePolicyID: policy.ID, ScheduledAt: &slot, ProjectID: project.ID, SourceRunID: run.ID, ServerID: server.ID, SourceResourceID: resource.ResourceID, State: "creating", Revision: 1, Policy: "retain", Location: "target-local", EncryptedInput: "encrypted-backup", CreatedAt: created, UpdatedAt: created}
		op := core.WorkloadBackupOperation{ID: id, BackupID: id, ProjectID: project.ID, CapturePolicyID: policy.ID, Action: "backup", State: "running", Revision: 1, LeaseToken: "capture", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted-operation", CreatedAt: created, UpdatedAt: created}
		check(s.AcceptWorkloadBackupCapture(ctx, policy, b, op))
		b.State, b.VerificationState, b.CleanupState, op.State = "ready", "verified", "complete", "succeeded"
		if i == 0 {
			b.Offsite = &core.BackupOffsiteArtifact{StoreID: objectStore.ID, ArchiveKey: "oldest/archive.enc", ManifestKey: "oldest/manifest.enc", ConfirmedAt: now}
		}
		check(s.CompleteWorkloadBackupOperation(ctx, b, op))
		policy, err = s.GetWorkloadBackupPolicy(ctx, policy.ID)
		check(err)
		policy.LastVerifiedBackupID, policy.State = id, "healthy"
		check(s.UpdateWorkloadBackupPolicy(ctx, policy, policy.Revision))
		policy, err = s.GetWorkloadBackupPolicy(ctx, policy.ID)
		check(err)
		if i == 0 {
			oldest, err = s.GetWorkloadBackup(ctx, id)
			check(err)
		}
	}
	for _, omitEvidence := range []bool{false, true} {
		candidate := oldest
		if omitEvidence {
			candidate.Offsite = nil
		}
		op := core.WorkloadBackupOperation{ID: ulid.Make().String(), BackupID: candidate.ID, ProjectID: project.ID, CapturePolicyID: policy.ID, Action: "delete", State: "running", Revision: 1, LeaseToken: "retention", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted-deletion", CreatedAt: now, UpdatedAt: now}
		if err = s.AcceptWorkloadBackupRetention(ctx, policy, candidate, op); !errors.Is(err, ErrWorkloadBackupChanged) {
			t.Fatalf("exported retention accepted, omitted caller evidence=%t: %v", omitEvidence, err)
		}
		if _, err = s.GetWorkloadBackupOperation(ctx, op.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("rejected retention left an accepted operation", err)
		}
	}
	policy.Enabled = false
	check(s.UpdateWorkloadBackupPolicy(ctx, policy, policy.Revision))
	manual := core.WorkloadBackupOperation{ID: ulid.Make().String(), BackupID: oldest.ID, ProjectID: project.ID, Action: "delete", State: "running", Revision: 1, LeaseToken: "manual", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted-deletion", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateWorkloadBackupOperation(ctx, manual, oldest.Revision); !errors.Is(err, ErrWorkloadBackupChanged) {
		t.Fatal("pausing retention allowed deletion of exported bytes", err)
	}
	if _, err = s.GetWorkloadBackupOperation(ctx, manual.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected manual deletion left an accepted operation", err)
	}
	saved, err := s.GetWorkloadBackup(ctx, oldest.ID)
	check(err)
	if saved.State != "ready" || saved.Revision != oldest.Revision || saved.Offsite == nil {
		t.Fatal("rejected deletion changed retained archive evidence")
	}
}

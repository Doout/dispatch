package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestWorkloadBackupPolicyOffsiteSQLiteAtomicExportAndRestart(t *testing.T) {
	testWorkloadBackupPolicyOffsite(t, filepath.Join(t.TempDir(), "offsite.db"))
}

func TestWorkloadBackupPolicyOffsitePostgresAtomicExportAndRestart(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testWorkloadBackupPolicyOffsite(t, dsn)
}

func testWorkloadBackupPolicyOffsite(t *testing.T, dsn string) {
	ctx := context.Background()
	s := mutationStore(t, dsn)
	defer s.Close()
	now := time.Now().UTC()
	prefix := ulid.Make().String()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	project := core.Project{ID: prefix + "project", Name: prefix, CreatedAt: now}
	server := core.Server{ID: prefix + "server", Name: prefix, Runtime: "docker", Address: "local", CreatedAt: now}
	check(s.CreateProject(ctx, project))
	check(s.CreateServer(ctx, server))
	check(s.CreateSecret(ctx, core.Secret{ID: prefix + "secret", Name: "Backup signing", Type: core.SecretTypeJSON, EncryptedValue: "encrypted-signing-fixture", CreatedAt: now, UpdatedAt: now}))
	destination := core.BackupObjectStore{ID: prefix + "store", ProjectID: project.ID, Name: "Approved store", CredentialSecretID: prefix + "secret", Config: backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "backups", Region: "us-east-1", Prefix: "dispatch", MaxBytes: 1 << 20}, CreatedAt: now}
	check(s.CreateBackupObjectStore(ctx, destination))
	run := core.ServiceProvisionRun{ID: prefix + "run", TemplateID: prefix + "template", ProjectID: project.ID, ServiceName: "pg", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: prefix}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: prefix + "service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Revision: 1, OperationID: run.ID, Policy: "retain", EncryptedRequest: "encrypted", CreatedAt: now, UpdatedAt: now}
	check(s.CreateServiceResource(ctx, run, resource))
	resource, err := s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "first", now, "")
	check(err)
	resource.State, resource.ResourceID = "ready", "container"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	check(s.SaveServiceResource(ctx, resource, run, nil))
	p := core.WorkloadBackupPolicy{ID: prefix + "policy", ProjectID: project.ID, SourceRunID: run.ID, SourceResourceID: resource.ResourceID, ServerID: server.ID, Name: "offsite", Enabled: true, Revision: 1, IntervalHours: 1, KeepLast: 1, NextCaptureAt: now, OffsiteStoreID: destination.ID, OffsiteStoreDigest: "frozen-destination", OffsiteStaleAfterHours: 2, EncryptedInput: "encrypted-policy", CreatedAt: now, UpdatedAt: now}
	check(s.CreateWorkloadBackupPolicy(ctx, p))
	b := core.WorkloadBackup{ID: prefix + "backup", CapturePolicyID: p.ID, ScheduledAt: &now, ProjectID: project.ID, SourceRunID: run.ID, SourceResourceID: resource.ResourceID, ServerID: server.ID, State: "creating", Revision: 1, Policy: "retain", Location: "target-local", EncryptedInput: "encrypted-archive-key", CreatedAt: now, UpdatedAt: now}
	capture := core.WorkloadBackupOperation{ID: b.ID, BackupID: b.ID, ProjectID: project.ID, CapturePolicyID: p.ID, Action: "backup", State: "running", Revision: 1, LeaseToken: "capture-token", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted-capture", CreatedAt: now, UpdatedAt: now}
	check(s.AcceptWorkloadBackupCapture(ctx, p, b, capture))
	capture.State, capture.CleanupState = "succeeded", "complete"
	b.State, b.VerificationState, b.CleanupState, b.Checksum = "ready", "verified", "complete", strings.Repeat("a", 64)
	b.VerifiedAt = &now
	check(s.CompleteWorkloadBackupOperation(ctx, b, capture))
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	makeExport := func() core.WorkloadBackupOperation {
		return core.WorkloadBackupOperation{ID: ulid.Make().String(), BackupID: b.ID, ProjectID: b.ProjectID, CapturePolicyID: p.ID, OffsiteStoreID: destination.ID, Action: "export", State: "running", Revision: 1, LeaseToken: "export-token", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted-object-grants", CreatedAt: now, UpdatedAt: now}
	}
	results := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.AcceptWorkloadBackupExport(ctx, p, b, makeExport()) }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d exports for the same capture", accepted)
	}
	check(s.Close())
	s, err = Open(ctx, dsn)
	check(err)
	check(s.Migrate(ctx))
	defer s.Close()
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	if b.ScheduledExportOperationID == "" || p.LastOffsiteOperationID != b.ScheduledExportOperationID {
		t.Fatal("restart lost original export identity")
	}
	if b.EncryptedInput != "encrypted-archive-key" || b.CapturePolicyID != p.ID || b.ScheduledAt == nil || !b.ScheduledAt.Equal(now) || b.State != "ready" {
		t.Fatal("scheduled export changed archive recovery input or capture provenance")
	}
	op, err := s.GetWorkloadBackupOperation(ctx, b.ScheduledExportOperationID)
	check(err)
	if op.EncryptedInput != "encrypted-object-grants" {
		t.Fatal("scheduled export lost its original encrypted grants")
	}
	op.State, op.CleanupState = "failed", "complete"
	check(s.CompleteWorkloadBackupOperation(ctx, b, op))
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	if s.AcceptWorkloadBackupExport(ctx, p, b, makeExport()) == nil {
		t.Fatal("failed original export accepted another automatic mutation")
	}
	results = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.MarkWorkloadBackupExportMissed(ctx, p, b) }()
	}
	wg.Wait()
	close(results)
	marked := 0
	for err := range results {
		if err == nil {
			marked++
		}
	}
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	if marked != 1 || p.MissedExports != 1 || !b.ScheduledExportMissed {
		t.Fatal("missed export counted more than once", marked, p.MissedExports)
	}
	if b.EncryptedInput != "encrypted-archive-key" || b.CapturePolicyID != p.ID || b.ScheduledAt == nil || !b.ScheduledAt.Equal(now) || b.State != "ready" {
		t.Fatal("missed export changed archive recovery input or capture provenance")
	}
	check(s.Close())
	s, err = Open(ctx, dsn)
	check(err)
	defer s.Close()
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	if s.MarkWorkloadBackupExportMissed(ctx, p, b) == nil || p.MissedExports != 1 || b.ScheduledExportOperationID != op.ID {
		t.Fatal("restart replaced export history")
	}
}

package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestWorkloadBackupPolicySQLiteAtomicSlotsAndRetention(t *testing.T) {
	testWorkloadBackupPolicyStore(t, filepath.Join(t.TempDir(), "policies.db"))
}
func TestWorkloadBackupPolicyPostgresAtomicSlotsAndRetention(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testWorkloadBackupPolicyStore(t, dsn)
}
func testWorkloadBackupPolicyStore(t *testing.T, dsn string) {
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
	project := core.Project{ID: prefix + "p", Name: prefix, CreatedAt: now}
	server := core.Server{ID: prefix + "s", Name: prefix, Runtime: "docker", Address: "local", CreatedAt: now}
	check(s.CreateProject(ctx, project))
	check(s.CreateServer(ctx, server))
	run := core.ServiceProvisionRun{ID: prefix + "r", TemplateID: prefix + "t", ProjectID: project.ID, ServiceName: "pg", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: prefix}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: prefix + "service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Revision: 1, OperationID: run.ID, Policy: "retain", EncryptedRequest: "encrypted", CreatedAt: now, UpdatedAt: now}
	check(s.CreateServiceResource(ctx, run, resource))
	resource, err := s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "first", now, "")
	check(err)
	resource.State, resource.ResourceID = "ready", "container"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	check(s.SaveServiceResource(ctx, resource, run, nil))
	p := core.WorkloadBackupPolicy{ID: prefix + "policy", ProjectID: project.ID, SourceRunID: run.ID, SourceResourceID: resource.ResourceID, ServerID: server.ID, Enabled: true, Revision: 1, IntervalHours: 1, KeepLast: 1, NextCaptureAt: now.Add(-3 * time.Hour), EncryptedInput: "encrypted policy", CreatedAt: now, UpdatedAt: now}
	check(s.CreateWorkloadBackupPolicy(ctx, p))
	slot, _, _ := p.DueCapture(now)
	makeCapture := func(id string, scheduled time.Time) (core.WorkloadBackup, core.WorkloadBackupOperation) {
		b := core.WorkloadBackup{ID: id, CapturePolicyID: p.ID, ScheduledAt: &scheduled, ProjectID: project.ID, SourceRunID: run.ID, ServerID: server.ID, SourceResourceID: resource.ResourceID, State: "creating", Revision: 1, Policy: "retain", Location: "target-local", EncryptedInput: "encrypted capture", CreatedAt: now, UpdatedAt: now}
		o := core.WorkloadBackupOperation{ID: id, BackupID: id, ProjectID: project.ID, CapturePolicyID: p.ID, Action: "backup", State: "running", Revision: 1, LeaseToken: "token", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted operation", CreatedAt: now, UpdatedAt: now}
		return b, o
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, o := makeCapture(ulid.Make().String(), slot)
			results <- s.AcceptWorkloadBackupCapture(ctx, p, b, o)
		}()
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
		t.Fatalf("accepted %d duplicate captures", accepted)
	}
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	if p.MissedCaptures != 3 || !p.NextCaptureAt.After(now) || p.LastBackupID == "" {
		t.Fatal("cadence not persisted", p.MissedCaptures, p.NextCaptureAt)
	}
	check(s.Close())
	s, err = Open(ctx, dsn)
	check(err)
	check(s.Migrate(ctx))
	defer s.Close()
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	// A pending accepted slot cannot be replayed after a controller restart.
	now = now.Add(time.Hour)
	p.NextCaptureAt = now.Add(-time.Hour)
	check(s.UpdateWorkloadBackupPolicy(ctx, p, p.Revision))
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	slot, _, _ = p.DueCapture(now)
	b, o := makeCapture(prefix+"next", slot)
	old, err := s.GetWorkloadBackup(ctx, p.LastBackupID)
	check(err)
	oldOp, err := s.GetWorkloadBackupOperation(ctx, old.ID)
	check(err)
	for _, state := range []string{"running", "unknown", "unresolved"} {
		prior := oldOp
		prior.State = state
		_, err = s.db.ExecContext(ctx, s.q(`UPDATE workload_backup_operations SET state=?,payload=? WHERE id=?`), state, jsonText(prior), oldOp.ID)
		check(err)
		if err = s.AcceptWorkloadBackupCapture(ctx, p, b, o); err == nil {
			t.Fatalf("%s previous mutation allowed another capture", state)
		}
		if _, err = s.GetWorkloadBackup(ctx, b.ID); err != ErrNotFound {
			t.Fatalf("rejected %s capture left an archive record: %v", state, err)
		}
		if _, err = s.GetWorkloadBackupOperation(ctx, o.ID); err != ErrNotFound {
			t.Fatalf("rejected %s capture left an operation record: %v", state, err)
		}
	}
	old.State, old.VerificationState = "ready", "verified"
	old.CleanupState = "complete"
	oldOp.State = "succeeded"
	check(s.CompleteWorkloadBackupOperation(ctx, old, oldOp))
	old, err = s.GetWorkloadBackup(ctx, old.ID)
	check(err)
	// The new archive must verify before it can justify deleting the last usable archive.
	check(s.AcceptWorkloadBackupCapture(ctx, p, b, o))
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	b.State = "ready"
	o.State = "succeeded"
	check(s.CompleteWorkloadBackupOperation(ctx, b, o))
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	deletion := core.WorkloadBackupOperation{ID: prefix + "delete", BackupID: old.ID, ProjectID: p.ProjectID, CapturePolicyID: p.ID, Action: "delete", State: "running", Revision: 1, LeaseToken: "prune", LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "encrypted deletion", CreatedAt: now, UpdatedAt: now}
	if err = s.AcceptWorkloadBackupRetention(ctx, p, old, deletion); err == nil {
		t.Fatal("unverified capture replaced last usable archive")
	}
	verify := deletion
	verify.ID, verify.BackupID, verify.Action = prefix+"verify", b.ID, "verify"
	check(s.CreateWorkloadBackupOperation(ctx, verify, b.Revision))
	b.VerificationState, b.CleanupState = "verified", "complete"
	verify.State = "succeeded"
	check(s.CompleteWorkloadBackupOperation(ctx, b, verify))
	b, err = s.GetWorkloadBackup(ctx, b.ID)
	check(err)
	p.LastVerifiedBackupID = b.ID
	check(s.UpdateWorkloadBackupPolicy(ctx, p, p.Revision))
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	check(s.AcceptWorkloadBackupRetention(ctx, p, old, deletion))
	protect := deletion
	protect.ID, protect.BackupID = prefix+"unsafe", b.ID
	if err = s.CreateWorkloadBackupOperation(ctx, protect, b.Revision); err == nil {
		t.Fatal("manual deletion bypassed enabled policy protection")
	}
	if err = s.AcceptWorkloadBackupRetention(ctx, p, b, protect); err == nil {
		t.Fatal("retention deleted protected newest archive")
	}
	p.Enabled = false
	check(s.UpdateWorkloadBackupPolicy(ctx, p, p.Revision))
	p, err = s.GetWorkloadBackupPolicy(ctx, p.ID)
	check(err)
	if err = s.AcceptWorkloadBackupRetention(ctx, p, old, deletion); err == nil {
		t.Fatal("paused policy continued retention")
	}
	old.State = "deleted"
	deletion.State = "succeeded"
	check(s.CompleteWorkloadBackupOperation(ctx, old, deletion))
	check(s.CreateWorkloadBackupOperation(ctx, protect, b.Revision))
}

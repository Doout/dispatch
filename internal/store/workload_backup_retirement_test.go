package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestWorkloadBackupRetirementSQLiteConcurrentLastCopyAndRestart(t *testing.T) {
	testBackupRetirementStore(t, filepath.Join(t.TempDir(), "retirement.db"))
}
func TestWorkloadBackupRetirementPostgresConcurrentLastCopyAndRestart(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testBackupRetirementStore(t, dsn)
}
func testBackupRetirementStore(t *testing.T, dsn string) {
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
	server := core.Server{ID: prefix + "s", Name: prefix, Runtime: "docker", Address: "source.example", CreatedAt: now}
	check(s.CreateProject(ctx, project))
	check(s.CreateServer(ctx, server))
	secret := core.Secret{ID: prefix + "secret", Name: prefix, Type: core.SecretTypeJSON, EncryptedValue: "wrapped-credential", CreatedAt: now, UpdatedAt: now}
	check(s.CreateSecret(ctx, secret))
	destination := core.BackupObjectStore{ID: prefix + "store", ProjectID: project.ID, CredentialSecretID: secret.ID, Config: backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "backup", Region: "us-east-1", Prefix: "owned", MaxBytes: 100, ConditionalDelete: true}, CreatedAt: now}
	check(s.CreateBackupObjectStore(ctx, destination))
	makeBackup := func(id string) core.WorkloadBackup {
		b := core.WorkloadBackup{ID: id, ProjectID: project.ID, ServerID: server.ID, SourceRunID: prefix + "source", State: "ready", Revision: 1, Policy: "retain", Location: "target-local", LocalState: "present", Checksum: strings.Repeat("a", 64), VerificationState: "verified", CleanupState: "complete", EncryptedInput: "retained-wrapped-key", CreatedAt: now, UpdatedAt: now, Offsite: &core.BackupOffsiteArtifact{StoreID: destination.ID, ArchiveKey: id + "/archive", ManifestKey: id + "/manifest", ManifestChecksum: strings.Repeat("b", 64), ConfirmedAt: now, VerifiedAt: &now, VerificationState: "verified"}}
		_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workload_backups(id,project_id,server_id,source_run_id,state,revision,input_cipher,payload,created_at,offsite_store_id,local_state) VALUES(?,?,?,?,?,?,?,?,?,?,?)`), b.ID, b.ProjectID, b.ServerID, b.SourceRunID, b.State, b.Revision, b.EncryptedInput, jsonText(b), stamp(now), destination.ID, "present")
		check(err)
		return b
	}
	makeOperation := func(b core.WorkloadBackup, action string) core.WorkloadBackupOperation {
		return core.WorkloadBackupOperation{ID: ulid.Make().String(), BackupID: b.ID, ProjectID: b.ProjectID, Action: action, OffsiteStoreID: destination.ID, State: "running", Revision: 1, LeaseToken: ulid.Make().String(), LeaseUntil: now.Add(31 * time.Minute), EncryptedInput: "frozen-reviewed-input", CreatedAt: now, UpdatedAt: now}
	}
	if s.postgres {
		tx, err := s.db.BeginTx(ctx, nil)
		check(err)
		check(s.lockBackupDestructiveProject(ctx, tx.Tx, core.WorkloadBackup{ProjectID: project.ID}))
		// Capture INSERT needs a project foreign-key KEY SHARE lock. A destructive
		// project lock must serialize peers without blocking that FK check.
		inserted := make(chan error, 1)
		captureCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		go func() {
			_, err := s.db.ExecContext(captureCtx, s.q(`INSERT INTO workload_backups(id,project_id,server_id,source_run_id,state,revision,input_cipher,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?)`), prefix+"foreign-key-capture", project.ID, server.ID, prefix+"capture-source", "creating", 1, "wrapped", "{}", stamp(now))
			inserted <- err
		}()
		result := <-inserted
		cancel()
		check(tx.Rollback())
		check(result)
		_, err = s.db.ExecContext(ctx, s.q(`DELETE FROM workload_backups WHERE id=?`), prefix+"foreign-key-capture")
		check(err)
	}
	first := makeBackup(prefix + "first")
	// An export upload acknowledgement is not independent restoration evidence.
	before := *first.Offsite
	first.Offsite.VerifiedAt = nil
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE workload_backups SET payload=? WHERE id=?`), jsonText(first), first.ID)
	check(err)
	if err = s.CreateWorkloadBackupOperation(ctx, makeOperation(first, "retire-local"), first.Revision); !errors.Is(err, ErrWorkloadBackupChanged) {
		t.Fatal("unverified export permitted local retirement", err)
	}
	first.Offsite = &before
	_, err = s.db.ExecContext(ctx, s.q(`UPDATE workload_backups SET payload=? WHERE id=?`), jsonText(first), first.ID)
	check(err)
	if err = s.DeleteServer(ctx, server.ID); err == nil {
		t.Fatal("target lost local backup protection")
	}
	retire := makeOperation(first, "retire-local")
	check(s.CreateWorkloadBackupOperation(ctx, retire, first.Revision))
	// Uncertain local cleanup keeps the original target protected across restart.
	retire.State = "unknown"
	retire.CleanupState = "pending"
	check(s.CompleteWorkloadBackupOperation(ctx, first, retire))
	first, err = s.GetWorkloadBackup(ctx, first.ID)
	check(err)
	if err = s.DeleteServer(ctx, server.ID); err == nil {
		t.Fatal("uncertain local retirement released target")
	}
	check(s.Close())
	s, err = Open(ctx, dsn)
	check(err)
	defer s.Close()
	claimed, err := s.ClaimWorkloadBackupRecovery(ctx, retire.ID, now.Add(32*time.Minute), "restarted-controller")
	check(err)
	first.LocalState = "retired"
	claimed.State = "succeeded"
	claimed.CleanupState = "complete"
	check(s.CompleteWorkloadBackupOperation(ctx, first, claimed))
	first, err = s.GetWorkloadBackup(ctx, first.ID)
	check(err)
	if first.EncryptedInput != "retained-wrapped-key" || first.Offsite == nil || first.LocalState != "retired" {
		t.Fatal("retirement discarded recovery metadata")
	}
	check(s.DeleteServer(ctx, server.ID))
	// The source may already be gone. Its last offsite recovery copy remains protected.
	if err = s.CreateWorkloadBackupOperation(ctx, makeOperation(first, "delete-offsite"), first.Revision); !errors.Is(err, ErrWorkloadBackupChanged) {
		t.Fatal("last usable recovery point deleted", err)
	}
	second := makeBackup(prefix + "second")
	second.LocalState = "retired"
	_, err = s.db.ExecContext(ctx, s.q(`UPDATE workload_backups SET payload=?,local_state='retired' WHERE id=?`), jsonText(second), second.ID)
	check(err)
	var wg sync.WaitGroup
	accepted := make(chan core.WorkloadBackupOperation, 2)
	for _, b := range []core.WorkloadBackup{first, second} {
		wg.Add(1)
		go func(b core.WorkloadBackup) {
			defer wg.Done()
			op := makeOperation(b, "delete-offsite")
			if s.CreateWorkloadBackupOperation(ctx, op, b.Revision) == nil {
				accepted <- op
			}
		}(b)
	}
	wg.Wait()
	close(accepted)
	var winner core.WorkloadBackupOperation
	count := 0
	for op := range accepted {
		count++
		winner = op
	}
	if count != 1 {
		t.Fatalf("accepted %d concurrent final-copy deletions", count)
	}
	selected, err := s.GetWorkloadBackup(ctx, winner.BackupID)
	check(err)
	winner.State = "unknown"
	winner.CleanupState = "pending"
	check(s.CompleteWorkloadBackupOperation(ctx, selected, winner))
	check(s.Close())
	s, err = Open(ctx, dsn)
	check(err)
	defer s.Close()
	recovered, err := s.ClaimWorkloadBackupRecovery(ctx, winner.ID, now.Add(32*time.Minute), "next-controller")
	check(err)
	selected, err = s.GetWorkloadBackup(ctx, selected.ID)
	check(err)
	selected.State = "deleted"
	selected.Offsite.DeletedAt = &now
	recovered.State = "succeeded"
	recovered.CleanupState = "complete"
	check(s.CompleteWorkloadBackupOperation(ctx, selected, recovered))
	survivor := first
	if survivor.ID == selected.ID {
		survivor = second
	}
	survivor, err = s.GetWorkloadBackup(ctx, survivor.ID)
	check(err)
	if err = s.CreateWorkloadBackupOperation(ctx, makeOperation(survivor, "delete-offsite"), survivor.Revision); !errors.Is(err, ErrWorkloadBackupChanged) {
		t.Fatal("restart allowed deletion of remaining recovery point", err)
	}
	// Both admission orders protect the last copy against mixed offsite and
	// ordinary local deletion. The local action retains its explicit semantics.
	local := makeBackup(prefix + "local-alternative")
	local.Offsite = nil
	_, err = s.db.ExecContext(ctx, s.q(`UPDATE workload_backups SET payload=?,offsite_store_id=NULL WHERE id=?`), jsonText(local), local.ID)
	check(err)
	for _, firstAction := range []string{"delete-offsite", "delete"} {
		offOp := makeOperation(survivor, "delete-offsite")
		localOp := makeOperation(local, "delete")
		primary, secondary, primaryBackup, secondaryBackup := offOp, localOp, survivor, local
		if firstAction == "delete" {
			primary, secondary, primaryBackup, secondaryBackup = localOp, offOp, local, survivor
		}
		check(s.CreateWorkloadBackupOperation(ctx, primary, primaryBackup.Revision))
		if err = s.CreateWorkloadBackupOperation(ctx, secondary, secondaryBackup.Revision); !errors.Is(err, ErrWorkloadBackupChanged) {
			t.Fatal("mixed deletion lost last-copy protection", firstAction, err)
		}
		primary.State = "failed"
		primary.CleanupState = "complete"
		check(s.CompleteWorkloadBackupOperation(ctx, primaryBackup, primary))
		local, err = s.GetWorkloadBackup(ctx, local.ID)
		check(err)
		survivor, err = s.GetWorkloadBackup(ctx, survivor.ID)
		check(err)
	}

	policy := core.WorkloadBackupPolicy{ID: prefix + "policy", ProjectID: project.ID, SourceRunID: survivor.SourceRunID, ServerID: server.ID, Enabled: true, Revision: 1, IntervalHours: 1, KeepLast: 1, OffsiteStoreID: destination.ID, LastBackupID: local.ID, LastVerifiedBackupID: local.ID, EncryptedInput: "wrapped-policy", CreatedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO workload_backup_policies(id,project_id,source_run_id,server_id,enabled,revision,input_cipher,payload,created_at) VALUES(?,?,?,?,?,?,?,?,?)`), policy.ID, policy.ProjectID, policy.SourceRunID, policy.ServerID, true, 1, policy.EncryptedInput, jsonText(policy), stamp(now))
	check(err)
	survivor.CapturePolicyID, local.CapturePolicyID = policy.ID, policy.ID
	local.CreatedAt = now.Add(time.Hour)
	for _, point := range []core.WorkloadBackup{survivor, local} {
		_, err = s.db.ExecContext(ctx, s.q(`UPDATE workload_backups SET payload=?,capture_policy_id=?,scheduled_at=? WHERE id=?`), jsonText(point), policy.ID, stamp(point.CreatedAt), point.ID)
		check(err)
	}
	// The new local point is outside the old archive's protected KeepLast set,
	// but it is not an independently verified replacement in the required store.
	if err = s.CreateWorkloadBackupOperation(ctx, makeOperation(survivor, "delete-offsite"), survivor.Revision); !errors.Is(err, ErrWorkloadBackupChanged) {
		t.Fatal("enabled offsite policy lost its required recovery point", err)
	}
	policy.Enabled = false
	_, err = s.db.ExecContext(ctx, s.q(`UPDATE workload_backup_policies SET enabled=FALSE,payload=? WHERE id=?`), jsonText(policy), policy.ID)
	check(err)
	pausedDelete := makeOperation(survivor, "delete-offsite")
	check(s.CreateWorkloadBackupOperation(ctx, pausedDelete, survivor.Revision))
	pausedDelete.State = "failed"
	pausedDelete.CleanupState = "complete"
	check(s.CompleteWorkloadBackupOperation(ctx, survivor, pausedDelete))
	selected, err = s.GetWorkloadBackup(ctx, selected.ID)
	check(err)
	if selected.EncryptedInput != "retained-wrapped-key" || selected.Offsite.DeletedAt == nil {
		t.Fatal("delete lost controller evidence")
	}
}

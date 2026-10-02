package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
)

func backupUnitRequest(t *testing.T) core.WorkloadBackupRequest {
	t.Helper()
	key, err := workloadbackup.Key()
	if err != nil {
		t.Fatal(err)
	}
	source, err := PrepareServiceRequest(core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "source", ProjectID: "project", TemplateID: "template", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: "server"}}, ServiceType: "postgresql"})
	if err != nil {
		t.Fatal(err)
	}
	return core.WorkloadBackupRequest{OperationID: "operation", Action: "backup", Key: key, Source: source, Backup: core.WorkloadBackup{ID: "backup", ArtifactID: "backup", ProjectID: "project", ServerID: "server", SourceRunID: "source", StorageID: "storage", Consistency: "database-native", Format: "postgresql-custom", Location: "target-local", Policy: "retain"}, Storage: core.StorageResource{ID: "storage", ServerID: "server", ProjectID: "project", ProvisionRunID: "source", Ownership: "verified", Kind: "docker_volume"}}
}

func TestWorkloadBackupRejectsConnectionStringsBeforeExecution(t *testing.T) {
	if err := ValidateWorkloadBackupRequest(backupUnitRequest(t), core.Server{ID: "server"}); err != nil {
		t.Fatal("invalid baseline request", err)
	}
	for _, value := range []string{"postgresql://other/database", "host=other dbname=database", "-hother", "database\x00suffix"} {
		t.Run(value, func(t *testing.T) {
			r := backupUnitRequest(t)
			r.Source.Inputs = map[string]string{"database": value}
			called := false
			e := DockerExecutor{WorkloadBackupDirectory: filepath.Join(t.TempDir(), "private"), run: func(context.Context, io.Reader, io.Writer, string, ...string) error { called = true; return nil }}
			if _, err := e.RunWorkloadBackup(context.Background(), r, core.Server{ID: "server", Address: "local", Runtime: "docker"}); err == nil || called {
				t.Fatal("connection string reached runtime", err, called)
			}
		})
	}
}

func TestWorkloadBackupCleanupFailureAndRecoveryPreserveOwnership(t *testing.T) {
	r := backupUnitRequest(t)
	r.Action = "verify"
	dir, err := privateBackupDirectory(filepath.Join(t.TempDir(), "private"), r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]string{"dispatch.project": r.Backup.ProjectID, "dispatch.workload-backup": r.Backup.ID, "dispatch.backup-operation": r.OperationID}
	failRemove := true
	exists := true
	removes := 0
	e := DockerExecutor{run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		switch args[0] {
		case "ps":
			if exists {
				io.WriteString(out, "owned-container")
			}
		case "inspect":
			raw, _ := json.Marshal(owned)
			out.Write(raw)
		case "rm":
			removes++
			if failRemove {
				return errors.New("daemon unavailable")
			}
			exists = false
		case "volume":
			if args[1] != "ls" {
				return errors.New("unexpected volume mutation")
			}
		default:
			return errors.New("unexpected runtime command")
		}
		return nil
	}}
	result, err := e.verifyWorkloadBackup(context.Background(), dir, strings.NewReader("unused"), r, workloadbackup.Artifact{ID: r.Backup.ID})
	if err == nil || result.State != "unknown" || result.CleanupState != "failed" {
		t.Fatal(result, err)
	}
	// Reconciliation must not delete a same-name resource whose ownership changed.
	r.Action, r.RecoveryAction = "reconcile", "verify"
	failRemove = false
	owned["dispatch.project"] = "another-project"
	before := removes
	result, err = e.reconcileWorkloadBackup(context.Background(), dir, r)
	if err == nil || result.CleanupState != "failed" || removes != before {
		t.Fatal("foreign cleanup attempted", result, err)
	}
	owned["dispatch.project"] = r.Backup.ProjectID
	result, err = e.reconcileWorkloadBackup(context.Background(), dir, r)
	if err != nil || result.State != "failed" || result.CleanupState != "complete" || exists {
		t.Fatal(result, err)
	}
}

func TestWorkloadBackupInterruptedRestoreNeverRunsAgainDuringRecovery(t *testing.T) {
	r := backupUnitRequest(t)
	r.Action, r.RecoveryAction = "reconcile", "restore"
	dir, err := privateBackupDirectory(filepath.Join(t.TempDir(), "private"), r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	e := DockerExecutor{run: func(context.Context, io.Reader, io.Writer, string, ...string) error { commands++; return nil }}
	result, err := e.reconcileWorkloadBackup(context.Background(), dir, r)
	if err != nil || result.State != "unresolved" || commands != 0 {
		t.Fatal("uncertain restore repeated", result, err, commands)
	}
	completed := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "restored", CleanupState: "complete"}
	if err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, completed); err != nil {
		t.Fatal(err)
	}
	result, err = e.reconcileWorkloadBackup(context.Background(), dir, r)
	if err != nil || result.State != "restored" || commands != 0 {
		t.Fatal("completed restore repeated", result, err, commands)
	}
}

func TestWorkloadBackupDeleteRetiresEmptyAcceptanceButRefusesUnknownBytes(t *testing.T) {
	r := backupUnitRequest(t)
	r.Action = "delete"
	dir, err := privateBackupDirectory(filepath.Join(t.TempDir(), "private"), r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "archive.enc"), []byte("unowned"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = (DockerExecutor{}).deleteWorkloadBackup(dir, r); err == nil {
		t.Fatal("unowned archive deleted")
	}
	if err = os.Remove(filepath.Join(dir, "archive.enc")); err != nil {
		t.Fatal(err)
	}
	r.Action, r.RecoveryAction = "reconcile", "backup"
	if err = os.WriteFile(filepath.Join(dir, ".metadata-interrupted"), []byte("unfinished encrypted metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (DockerExecutor{}).reconcileWorkloadBackup(context.Background(), dir, r)
	if err != nil || result.State != "failed" {
		t.Fatal("interrupted acceptance cannot be reconciled", result, err)
	}
	r.Action, r.RecoveryAction = "delete", ""
	result, err = (DockerExecutor{}).deleteWorkloadBackup(dir, r)
	if err != nil || result.State != "deleted" {
		t.Fatal("empty acceptance cannot be retired", result, err)
	}
}

func TestWorkloadBackupReceiptRejectsChangedActionAndCorruption(t *testing.T) {
	r := backupUnitRequest(t)
	root := filepath.Join(t.TempDir(), "private")
	dir, err := privateBackupDirectory(root, r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	completed := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "ready", CleanupState: "complete"}
	name := "result-" + r.OperationID + ".enc"
	if err = writeBackupOperationResult(dir, name, r, completed); err != nil {
		t.Fatal(err)
	}
	commands := 0
	e := DockerExecutor{WorkloadBackupDirectory: root, run: func(context.Context, io.Reader, io.Writer, string, ...string) error { commands++; return nil }}
	server := core.Server{ID: "server", Address: "local", Runtime: "docker"}
	r.Backup.State, r.Backup.Revision, r.Backup.Checksum = "ready", 9, strings.Repeat("a", 64)
	result, err := e.RunWorkloadBackup(context.Background(), r, server)
	if err != nil || result.State != "ready" || commands != 0 {
		t.Fatal("mutable metadata broke legitimate replay", result, err)
	}
	r.Action = "delete"
	if _, err = e.RunWorkloadBackup(context.Background(), r, server); err == nil || commands != 0 {
		t.Fatal("changed accepted action was replayed", err)
	}
	r.Action = "backup"
	r.Source.Inputs = map[string]string{"database": "different"}
	if _, err = e.RunWorkloadBackup(context.Background(), r, server); err == nil || commands != 0 {
		t.Fatal("changed accepted source was replayed", err)
	}
	r.Source.Inputs = nil
	if err = os.WriteFile(filepath.Join(dir, name), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = e.RunWorkloadBackup(context.Background(), r, server)
	if err == nil || result.State != "unknown" || commands != 0 {
		t.Fatal("corrupt receipt repeated execution", result, err)
	}
}

func TestWorkloadBackupReconcileAllowsReviewedRetirementOfOwnedIncompleteArchive(t *testing.T) {
	r := backupUnitRequest(t)
	r.Action, r.RecoveryAction = "reconcile", "backup"
	dir, err := privateBackupDirectory(filepath.Join(t.TempDir(), "private"), r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := workloadbackup.Artifact{ID: r.Backup.ID, ProjectID: r.Backup.ProjectID, RequestDigest: backupRequestDigest(r)}
	if err = writeBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, owner); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "archive.enc"), []byte("interrupted archive"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := (DockerExecutor{}).reconcileWorkloadBackup(context.Background(), dir, r)
	if err != nil || result.State != "failed" {
		t.Fatal("owned incomplete archive cannot be retired", result, err)
	}
	r.OperationID, r.Action, r.RecoveryAction = "reviewed-deletion", "delete", ""
	result, err = (DockerExecutor{}).deleteWorkloadBackup(dir, r)
	if err != nil || result.State != "deleted" {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "archive.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive remained after reviewed deletion", err)
	}
}

func TestWorkloadBackupDatabaseTimeoutUsesRemainingDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	calls := 0
	e := DockerExecutor{run: func(_ context.Context, _ io.Reader, _ io.Writer, _ string, args ...string) error {
		calls++
		found := false
		for _, arg := range args {
			if strings.HasPrefix(arg, "PGOPTIONS=") {
				found = true
				parts := strings.Fields(arg)
				millis, err := strconv.ParseInt(strings.TrimPrefix(parts[1], "transaction_timeout="), 10, 64)
				if err != nil || millis < 1 || millis > 1999 {
					t.Fatalf("late SQL launch received fresh 25 minute budget: %q", arg)
				}
				if !strings.Contains(arg, "statement_timeout="+strconv.FormatInt(millis, 10)) {
					t.Fatal("statement timeout differs", arg)
				}
			}
		}
		if !found {
			t.Fatal("database launch omitted bounded timeout", args)
		}
		return nil
	}}
	if err := e.restoreInto(ctx, strings.NewReader("archive"), "container", "dispatch", "database", nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := e.restoreInto(ctx, strings.NewReader("archive"), "container", "dispatch", "database", nil); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatal("expired context launched database work", calls, err)
	}
	options, err := backupPostgresOptions(context.Background())
	if err != nil || !strings.Contains(options, "transaction_timeout=1500000") {
		t.Fatal("maximum SQL bound missing", options, err)
	}
}

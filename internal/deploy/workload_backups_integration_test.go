package deploy

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
	"github.com/oklog/ulid/v2"
)

func TestWorkloadBackupPostgresIsolatedVerificationAndRestoreIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	executor := DockerExecutor{WorkloadBackupDirectory: filepath.Join(t.TempDir(), "backups")}
	server := core.Server{ID: "backup-target", Runtime: "docker", Address: "local"}
	create := func() (core.ServiceProvisionRequest, core.StorageResource, string) {
		t.Helper()
		id := strings.ToLower(ulid.Make().String())
		name := ServiceResourceName(id)
		network := "backup-test-" + id
		t.Cleanup(func() {
			exec.Command("docker", "rm", "-f", name).Run()
			exec.Command("docker", "volume", "rm", name+"-data").Run()
			exec.Command("docker", "network", "rm", network).Run()
		})
		run := core.ServiceProvisionRun{ID: id, TemplateID: "postgres", ProjectID: "backup-project", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: name, Network: network}}
		req, err := PrepareServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Outputs: []string{"connectionUrl"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = executor.Provision(ctx, req, core.DockerServiceProvision{Network: network}, server); err != nil {
			t.Fatal(err)
		}
		inspected, err := executor.InspectServiceResource(ctx, req, server)
		if err != nil {
			t.Fatal(err)
		}
		items, err := (RuntimeStorage{}).Inspect(ctx, server)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range items {
			if item.Resource.Name == name+"-data" {
				s := item.Resource
				s.ID = "storage-" + id
				s.ServerID = server.ID
				s.ProjectID = run.ProjectID
				s.ProvisionRunID = run.ID
				s.Ownership = "verified"
				return req, s, inspected.ResourceID
			}
		}
		t.Fatal("owned volume missing")
		return req, core.StorageResource{}, ""
	}
	source, storage, container := create()
	query := func(container, sql string) string {
		t.Helper()
		out, err := executor.backupOutput(ctx, "exec", container, "psql", "-U", "dispatch", "-d", "database", "-tAc", sql)
		if err != nil {
			t.Fatal("fixture database query failed", err)
		}
		return out
	}
	query(container, "CREATE TABLE backup_marker(value text); INSERT INTO backup_marker VALUES ('original')")
	key, _ := workloadbackup.Key()
	backupID := ulid.Make().String()
	record := core.WorkloadBackup{ID: backupID, ProjectID: source.Run.ProjectID, ServerID: server.ID, SourceRunID: source.Run.ID, StorageID: storage.ID, SourceResourceID: container, ArtifactID: backupID, Consistency: "database-native", Format: "postgresql-custom", Location: "target-local", Policy: "retain"}
	request := core.WorkloadBackupRequest{OperationID: ulid.Make().String(), Action: "backup", Backup: record, Source: source, Storage: storage, Key: key, Checks: []core.BackupIntegrityCheck{{Query: "SELECT value FROM backup_marker", Expected: "original"}}}
	created, err := executor.RunWorkloadBackup(ctx, request, server)
	if err != nil || created.State != "ready" || created.Bytes == 0 {
		t.Fatal(created, err)
	}
	request.Backup.Checksum = created.Checksum
	replay, err := executor.RunWorkloadBackup(ctx, request, server)
	if err != nil || replay.Checksum != created.Checksum {
		t.Fatal("backup replay changed artifact", err)
	}
	query(container, "UPDATE backup_marker SET value='live-change'")
	request.Action, request.OperationID = "verify", ulid.Make().String()
	verified, err := executor.RunWorkloadBackup(ctx, request, server)
	if err != nil || verified.State != "verified" || verified.CleanupState != "complete" {
		t.Fatal(verified, err)
	}
	if query(container, "SELECT value FROM backup_marker") != "live-change" {
		t.Fatal("verification changed live data")
	}
	destination, destinationStorage, target := create()
	query(target, "CREATE TABLE backup_marker(value text); INSERT INTO backup_marker VALUES ('destination')")
	request.Action, request.OperationID, request.Destination, request.DestinationStorage, request.ExpectedDestination = "restore", ulid.Make().String(), &destination, &destinationStorage, target
	restored, err := executor.RunWorkloadBackup(ctx, request, server)
	if err != nil || restored.State != "restored" || query(target, "SELECT value FROM backup_marker") != "original" {
		t.Fatal(restored, err)
	}
	if query(container, "SELECT value FROM backup_marker") != "live-change" {
		t.Fatal("restore changed source")
	}
	wrong := request
	wrong.OperationID = ulid.Make().String()
	wrong.Key, _ = workloadbackup.Key()
	if _, err = executor.RunWorkloadBackup(ctx, wrong, server); err == nil {
		t.Fatal("wrong encryption key accepted")
	}
	dir, _ := workloadbackup.Path(executor.WorkloadBackupDirectory, backupID)
	f, err := os.OpenFile(filepath.Join(dir, "archive.enc"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Seek(100, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("corrupt"))
	f.Close()
	request.OperationID = ulid.Make().String()
	if _, err = executor.RunWorkloadBackup(ctx, request, server); err == nil {
		t.Fatal("corrupt archive changed destination")
	}
	if query(target, "SELECT value FROM backup_marker") != "original" {
		t.Fatal("failed validation mutated destination")
	}
}

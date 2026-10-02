package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/workloadbackup"
	"github.com/oklog/ulid/v2"
)

func TestWorkloadBackupAgentRestartAndIsolatedVerificationIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_RUNTIME_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_RUNTIME_INTEGRATION=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	id := strings.ToLower(ulid.Make().String())
	name := deploy.ServiceResourceName(id)
	network := "backup-agent-" + id
	t.Cleanup(func() {
		exec.Command("docker", "rm", "-f", name).Run()
		exec.Command("docker", "volume", "rm", name+"-data").Run()
		exec.Command("docker", "network", "rm", network).Run()
	})
	server := core.Server{ID: "target", AgentNodeID: "node", Address: "agent:node", Runtime: "docker"}
	source, _ := deploy.PrepareServiceRequest(core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: id, TemplateID: "postgres", ProjectID: "project", ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: name, Network: network}}, ServiceType: "postgresql", Outputs: []string{"connectionUrl"}})
	directory := filepath.Join(t.TempDir(), "worker")
	worker, err := Open(directory, "node")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { worker.Close() }()
	execute := func(request remoteruntime.Request) (remoteruntime.Result, remoteruntime.LeasedJob) {
		t.Helper()
		raw, _ := json.Marshal(request)
		sum := sha256.Sum256(raw)
		job := remoteruntime.LeasedJob{ID: ulid.Make().String(), Attempt: 1, Digest: hex.EncodeToString(sum[:]), ExpiresAt: time.Now().Add(2 * time.Minute), Request: request}
		result := worker.Run(ctx, job, nil)
		if result.State != "succeeded" {
			t.Fatal(result.Code, result.Message)
		}
		return result, job
	}
	execute(remoteruntime.NewServiceRequest(source, core.DockerServiceProvision{ServerRef: server.ID, Network: network}, server))
	query := func(sql string) string {
		t.Helper()
		out, err := exec.Command("docker", "exec", name, "psql", "-U", "dispatch", "-d", "database", "-tAc", sql).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	query("CREATE TABLE backup_marker(value text); INSERT INTO backup_marker VALUES('before')")
	local := server
	local.AgentNodeID, local.Address = "", "local"
	items, err := (deploy.RuntimeStorage{}).Inspect(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	var storage core.StorageResource
	for _, item := range items {
		if item.Resource.Name == name+"-data" {
			storage = item.Resource
		}
	}
	storage.ID, storage.ServerID, storage.ProjectID, storage.ProvisionRunID, storage.Ownership = "storage", server.ID, source.Run.ProjectID, source.Run.ID, "verified"
	inspected, err := (deploy.DockerExecutor{}).InspectServiceResource(ctx, source, local)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := workloadbackup.Key()
	backupID := ulid.Make().String()
	b := core.WorkloadBackup{ID: backupID, ArtifactID: backupID, ProjectID: source.Run.ProjectID, ServerID: server.ID, NodeID: server.AgentNodeID, SourceRunID: source.Run.ID, SourceResourceID: inspected.ResourceID, StorageID: storage.ID, Consistency: "database-native", Format: "postgresql-custom", Location: "target-local", Policy: "retain"}
	input := core.WorkloadBackupRequest{OperationID: ulid.Make().String(), Action: "backup", Backup: b, Source: source, Storage: storage, Key: key, Checks: []core.BackupIntegrityCheck{{Query: "SELECT value FROM backup_marker", Expected: "before"}}}
	created, job := execute(remoteruntime.NewWorkloadBackupRequest(input, server))
	if created.WorkloadBackup == nil || created.WorkloadBackup.Checksum == "" {
		t.Fatal("agent lost artifact evidence")
	}
	worker.Close()
	worker, err = Open(directory, "node")
	if err != nil {
		t.Fatal(err)
	}
	replay := worker.Run(ctx, job, nil)
	if replay.State != "succeeded" || replay.WorkloadBackup == nil || replay.WorkloadBackup.Checksum != created.WorkloadBackup.Checksum {
		t.Fatal("restart changed accepted backup result")
	}
	query("UPDATE backup_marker SET value='live'")
	input.Backup.Checksum = created.WorkloadBackup.Checksum
	input.Action, input.OperationID = "verify", ulid.Make().String()
	verified, _ := execute(remoteruntime.NewWorkloadBackupRequest(input, server))
	if verified.WorkloadBackup.State != "verified" || verified.WorkloadBackup.CleanupState != "complete" || query("SELECT value FROM backup_marker") != "live" {
		t.Fatal("agent verification changed live data or retained temporary resources")
	}
	// Public operation metadata never includes the accepted archive encryption key.
	raw, _ := json.Marshal(verified)
	if strings.Contains(string(raw), key) || strings.Contains(string(raw), source.Password) {
		t.Fatal("agent result exposed encryption or database credentials")
	}
}

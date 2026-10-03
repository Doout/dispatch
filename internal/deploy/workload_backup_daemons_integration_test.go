package deploy

import (
	"context"
	"errors"
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

// Separate nested daemons exercise independent image, volume and container
// inventories. They share a physical host and use a fixture object store, so this
// does not establish independent-host or live object-store acceptance.
func TestWorkloadBackupSeparateDaemonsSourceLossIntegration(t *testing.T) {
	if os.Getenv("DISPATCH_OFFSITE_DAEMON_INTEGRATION") != "1" {
		t.Skip("set DISPATCH_OFFSITE_DAEMON_INTEGRATION=1 for disposable nested Docker daemons")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	owner := "dispatch-backup-daemons-" + strings.ToLower(ulid.Make().String())
	hostEndpoint := os.Getenv("DOCKER_HOST")
	if hostEndpoint == "" || os.Getenv("DOCKER_CONTEXT") != "" {
		out, err := exec.CommandContext(ctx, "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
		if err != nil {
			t.Fatal("resolve fixture Docker endpoint", err)
		}
		hostEndpoint = strings.TrimSpace(string(out))
	}
	if !strings.HasPrefix(hostEndpoint, "unix://") {
		t.Fatal("nested-daemon acceptance requires a local Docker Unix socket")
	}
	command := func(commandCtx context.Context, endpoint string, input io.Reader, output io.Writer, args ...string) error {
		cmd := exec.CommandContext(commandCtx, "docker", append([]string{"--host", endpoint}, args...)...)
		for _, value := range os.Environ() {
			if !strings.HasPrefix(value, "DOCKER_HOST=") && !strings.HasPrefix(value, "DOCKER_CONTEXT=") && !strings.HasPrefix(value, "DOCKER_TLS_VERIFY=") && !strings.HasPrefix(value, "DOCKER_CERT_PATH=") {
				cmd.Env = append(cmd.Env, value)
			}
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = input, output, io.Discard
		return cmd.Run()
	}
	docker := func(endpoint string, args ...string) string {
		t.Helper()
		var out strings.Builder
		if err := command(ctx, endpoint, nil, &out, args...); err != nil {
			t.Fatalf("fixture Docker %s failed: %v", args[0], err)
		}
		return strings.TrimSpace(out.String())
	}
	docker(hostEndpoint, "pull", "docker:29-dind")
	image := docker(hostEndpoint, "image", "inspect", "--format", "{{.Id}}", "docker:29-dind")
	newDaemon := func(suffix string) (string, func()) {
		t.Helper()
		name, volume := owner+"-"+suffix, owner+"-"+suffix+"-data"
		if command(ctx, hostEndpoint, nil, io.Discard, "container", "inspect", name) == nil || command(ctx, hostEndpoint, nil, io.Discard, "volume", "inspect", volume) == nil {
			t.Fatal("fixture resource name already exists")
		}
		createdContainer, createdVolume := false, false
		remove := func() {
			t.Helper()
			cleanup, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			for _, resource := range []struct {
				kind, name string
				created    *bool
			}{{"container", name, &createdContainer}, {"volume", volume, &createdVolume}} {
				if !*resource.created {
					continue
				}
				var label strings.Builder
				format := "{{index .Config.Labels \"dispatch.test.owner\"}}"
				if resource.kind == "volume" {
					format = "{{index .Labels \"dispatch.test.owner\"}}"
				}
				if err := command(cleanup, hostEndpoint, nil, &label, resource.kind, "inspect", "--format", format, resource.name); err != nil || strings.TrimSpace(label.String()) != owner {
					t.Errorf("refusing cleanup without matching fixture ownership: %s", resource.kind)
					continue
				}
				args := []string{resource.kind, "rm", resource.name}
				if resource.kind == "container" {
					args = []string{"container", "rm", "-f", "-v", resource.name}
				}
				if err := command(cleanup, hostEndpoint, nil, io.Discard, args...); err != nil {
					t.Errorf("fixture cleanup %s failed: %v", resource.kind, err)
					continue
				}
				*resource.created = false
			}
		}
		t.Cleanup(remove)
		createdVolume = true
		docker(hostEndpoint, "volume", "create", "--label", "dispatch.test.owner="+owner, volume)
		createdContainer = true
		docker(hostEndpoint, "run", "-d", "--name", name, "--label", "dispatch.test.owner="+owner, "--privileged", "--memory", "1g", "--cpus", "1", "--env", "DOCKER_TLS_CERTDIR=", "--mount", "type=volume,source="+volume+",target=/var/lib/docker", "--publish", "127.0.0.1::2375", image, "--tls=false")
		port := docker(hostEndpoint, "port", name, "2375/tcp")
		if !strings.HasPrefix(port, "127.0.0.1:") || strings.ContainsAny(port, "\r\n") {
			t.Fatal("fixture daemon was not bound only to loopback")
		}
		endpoint := "tcp://" + port
		readyBy := time.Now().Add(90 * time.Second)
		for command(ctx, endpoint, nil, io.Discard, "info") != nil {
			if ctx.Err() != nil || time.Now().After(readyBy) {
				t.Fatal("fixture daemon did not become ready")
			}
			time.Sleep(time.Second)
		}
		docker(endpoint, "pull", "postgres:17-bookworm")
		return endpoint, remove
	}
	sourceEndpoint, removeSource := newDaemon("source")
	destinationEndpoint, _ := newDaemon("destination")
	if docker(sourceEndpoint, "info", "--format", "{{.ID}}") == docker(destinationEndpoint, "info", "--format", "{{.ID}}") {
		t.Fatal("fixture targets share a daemon identity")
	}
	newExecutor := func(endpoint, directory string) DockerExecutor {
		return DockerExecutor{WorkloadBackupDirectory: directory, run: func(ctx context.Context, in io.Reader, out io.Writer, binary string, args ...string) error {
			if binary != "docker" {
				return errors.New("unexpected binary in backup fixture")
			}
			return command(ctx, endpoint, in, out, args...)
		}}
	}
	createDatabase := func(endpoint, id string) (core.ServiceProvisionRequest, core.StorageResource, string, core.Server) {
		t.Helper()
		t.Setenv("DOCKER_HOST", endpoint)
		t.Setenv("DOCKER_CONTEXT", "")
		t.Setenv("DOCKER_TLS_VERIFY", "")
		t.Setenv("DOCKER_CERT_PATH", "")
		name, network := ServiceResourceName(id), owner+"-network-"+id
		server := core.Server{ID: id, Runtime: core.ServerRuntimeDocker, Address: "local"}
		run := core.ServiceProvisionRun{ID: id, TemplateID: "postgres", ProjectID: owner, ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: id, ResourceName: name, Network: network}}
		request, err := PrepareServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Outputs: []string{"connectionUrl"}})
		if err != nil {
			t.Fatal("prepare fixture database", err)
		}
		if _, err = (DockerExecutor{}).Provision(ctx, request, core.DockerServiceProvision{Network: network}, server); err != nil {
			t.Fatal("provision fixture database", err)
		}
		resource, err := (DockerExecutor{}).InspectServiceResource(ctx, request, server)
		if err != nil {
			t.Fatal("inspect fixture database", err)
		}
		volumes, err := (RuntimeStorage{}).Inspect(ctx, server)
		if err != nil {
			t.Fatal("inspect fixture storage", err)
		}
		for _, volume := range volumes {
			if volume.Resource.Name == name+"-data" {
				storage := volume.Resource
				storage.ID, storage.ProjectID, storage.ServerID, storage.ProvisionRunID, storage.Ownership = "storage-"+id, owner, id, id, "verified"
				return request, storage, resource.ResourceID, server
			}
		}
		t.Fatal("fixture database volume missing")
		return core.ServiceProvisionRequest{}, core.StorageResource{}, "", server
	}
	source, sourceStorage, sourceContainer, sourceServer := createDatabase(sourceEndpoint, "source-"+strings.ToLower(ulid.Make().String()))
	destination, destinationStorage, destinationContainer, destinationServer := createDatabase(destinationEndpoint, "destination-"+strings.ToLower(ulid.Make().String()))
	if command(ctx, destinationEndpoint, nil, io.Discard, "container", "inspect", sourceContainer) == nil {
		t.Fatal("destination daemon can inspect a source container")
	}
	sourceDirectory := filepath.Join(t.TempDir(), "source-archives")
	sourceExecutor := newExecutor(sourceEndpoint, sourceDirectory)
	query := func(executor DockerExecutor, container, sql string) string {
		t.Helper()
		value, err := executor.backupOutput(ctx, "exec", container, "psql", "-U", "dispatch", "-d", "database", "-tAc", sql)
		if err != nil {
			t.Fatal("fixture database query failed", err)
		}
		return value
	}
	query(sourceExecutor, sourceContainer, "CREATE TABLE recovery_marker(value text); INSERT INTO recovery_marker VALUES ('survives-source-daemon-loss')")
	backupID := ulid.Make().String()
	key, err := workloadbackup.Key()
	if err != nil {
		t.Fatal(err)
	}
	record := core.WorkloadBackup{ID: backupID, ProjectID: owner, ServerID: sourceServer.ID, SourceRunID: source.Run.ID, StorageID: sourceStorage.ID, SourceResourceID: sourceContainer, ArtifactID: backupID, Consistency: "database-native", Format: "postgresql-custom", Location: "target-local", Policy: "retain"}
	request := core.WorkloadBackupRequest{OperationID: ulid.Make().String(), Action: "backup", Backup: record, Source: source, Storage: sourceStorage, Key: key, Checks: []core.BackupIntegrityCheck{{Query: "SELECT value FROM recovery_marker", Expected: "survives-source-daemon-loss"}}}
	created, err := sourceExecutor.RunWorkloadBackup(ctx, request, sourceServer)
	if err != nil || created.State != "ready" {
		t.Fatal("source backup did not complete", err)
	}
	request.Backup.Checksum, request.Backup.Bytes, request.Backup.ImageID = created.Checksum, created.Bytes, created.ImageID
	objects, access := backupObjectFixture(t, owner, backupID)
	sourceExecutor.BackupObjectClient = objects.Client()
	request.Action, request.OperationID, request.OffsiteAccess = "export", ulid.Make().String(), &access
	exported, err := sourceExecutor.RunWorkloadBackup(ctx, request, sourceServer)
	if err != nil || exported.State != "exported" || exported.Offsite == nil {
		t.Fatal("source export did not complete", err)
	}
	removeSource()
	if command(ctx, sourceEndpoint, nil, io.Discard, "info") == nil {
		t.Fatal("source daemon remains available")
	}
	if err = os.RemoveAll(sourceDirectory); err != nil {
		t.Fatal("remove source-local backup bytes", err)
	}
	destinationExecutor := newExecutor(destinationEndpoint, filepath.Join(t.TempDir(), "destination-archives"))
	destinationExecutor.BackupObjectClient = objects.Client()
	request.Backup.Offsite, request.Destination, request.DestinationStorage, request.ExpectedDestination = exported.Offsite, &destination, &destinationStorage, destinationContainer
	request.Action, request.OperationID = "verify", ulid.Make().String()
	verified, err := destinationExecutor.RunWorkloadBackup(ctx, request, destinationServer)
	if err != nil || verified.State != "verified" || verified.CleanupState != "complete" {
		t.Fatal("offsite verification failed after source-daemon removal", err)
	}
	request.Action, request.OperationID = "restore", ulid.Make().String()
	restored, err := destinationExecutor.RunWorkloadBackup(ctx, request, destinationServer)
	if err != nil || restored.State != "restored" {
		t.Fatal("offsite restore failed after source-daemon removal", err)
	}
	if query(destinationExecutor, destinationContainer, "SELECT value FROM recovery_marker") != "survives-source-daemon-loss" {
		t.Fatal("restored database differs from captured source")
	}
}

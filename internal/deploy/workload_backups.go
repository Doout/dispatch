package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
)

var backupImageID = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func ValidateWorkloadBackupRequest(r core.WorkloadBackupRequest, server core.Server) error {
	b := r.Backup
	if _, err := workloadbackup.Path("/unused", b.ID); err != nil {
		return err
	}
	if _, err := workloadbackup.Path("/unused", r.OperationID); err != nil {
		return err
	}
	if b.ProjectID == "" || b.ServerID != server.ID || b.ArtifactID != b.ID || b.Consistency != "database-native" || b.Format != "postgresql-custom" || b.Location != "target-local" || b.Policy != "retain" || b.SourceRunID != r.Source.Run.ID || r.Source.Run.ProjectID != b.ProjectID || r.Source.ServiceType != "postgresql" || r.Source.Run.Target == nil || r.Source.Run.Target.Provider != "docker" || r.Source.Run.Target.ServerID != server.ID || r.Source.Password == "" {
		return errors.New("backup source ownership or format is invalid")
	}
	if r.Storage.ID != b.StorageID || r.Storage.ServerID != server.ID || r.Storage.ProjectID != b.ProjectID || r.Storage.ProvisionRunID != b.SourceRunID || r.Storage.Ownership != "verified" || r.Storage.Kind != "docker_volume" {
		return errors.New("backup requires verified owned PostgreSQL storage")
	}
	raw, err := hex.DecodeString(r.Key)
	clear(raw)
	if err != nil || len(raw) != 32 {
		return errors.New("backup encryption key is unavailable")
	}
	if len(r.Checks) > 16 {
		return errors.New("at most 16 verification assertions are supported")
	}
	for _, check := range r.Checks {
		q := strings.TrimSpace(check.Query)
		if len(q) > 4096 || len(check.Expected) > 4096 || !strings.HasPrefix(strings.ToLower(q), "select ") || strings.ContainsAny(q, ";\x00") {
			return errors.New("verification assertions require one SELECT query")
		}
	}
	switch r.Action {
	case "backup", "inspect", "verify", "delete":
	case "reconcile":
		if r.RecoveryAction != "backup" && r.RecoveryAction != "verify" && r.RecoveryAction != "restore" && r.RecoveryAction != "delete" {
			return errors.New("invalid backup recovery action")
		}
	case "restore":
		if r.Destination == nil || r.DestinationStorage == nil || r.ExpectedDestination == "" || r.Destination.ServiceType != "postgresql" || r.Destination.Run.ProjectID != b.ProjectID || r.Destination.Run.Target == nil || r.Destination.Run.Target.ServerID != server.ID || r.Destination.Run.Target.Provider != "docker" || r.DestinationStorage.ServerID != server.ID || r.DestinationStorage.ProjectID != b.ProjectID || r.DestinationStorage.ProvisionRunID != r.Destination.Run.ID || r.DestinationStorage.Ownership != "verified" {
			return errors.New("restore destination ownership is invalid")
		}
	default:
		return errors.New("unsupported workload backup operation")
	}
	return nil
}

// Binary database streams must never mix stderr with stdout.
func (e DockerExecutor) backupCommand(ctx context.Context, in io.Reader, out io.Writer, args ...string) error {
	if e.run != nil {
		return e.run(ctx, in, out, "docker", args...)
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, io.Discard
	return cmd.Run()
}
func (e DockerExecutor) backupOutput(ctx context.Context, args ...string) (string, error) {
	var out boundedStorageOutput
	err := e.backupCommand(ctx, nil, &out, args...)
	return strings.TrimSpace(out.String()), err
}
func privateBackupDirectory(root, id string) (string, error) {
	dir, err := workloadbackup.Path(root, id)
	if err != nil {
		return "", err
	}
	for _, p := range []string{root, dir} {
		if err = os.MkdirAll(p, 0700); err != nil {
			return "", err
		}
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("backup directory must be private and cannot be a symlink")
		}
	}
	return dir, nil
}
func writeBackupJSON(dir, name, key, id string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	defer clear(raw)
	f, err := os.CreateTemp(dir, ".metadata-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, _, err = workloadbackup.Encrypt(f, bytes.NewReader(raw), key, id+":"+name); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func readBackupJSON(dir, name, key, id string, value any) error {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer f.Close()
	var raw bytes.Buffer
	if _, _, err = workloadbackup.Decrypt(&raw, io.LimitReader(f, 1<<20), key, id+":"+name); err != nil {
		return err
	}
	defer clear(raw.Bytes())
	return json.Unmarshal(raw.Bytes(), value)
}
func (e DockerExecutor) validateBackupSource(ctx context.Context, r core.WorkloadBackupRequest, server core.Server) (string, error) {
	inspected, err := e.InspectServiceResource(ctx, r.Source, server)
	if err != nil || inspected.State != "ready" || inspected.ResourceID != r.Backup.SourceResourceID {
		return "", errors.New("backup source is unavailable or its owned identity changed")
	}
	if err = e.validateBackupVolume(ctx, r.Storage); err != nil {
		return "", err
	}
	image, err := e.backupOutput(ctx, "inspect", "--format", "{{.Image}}", inspected.ResourceID)
	if err != nil || !backupImageID.MatchString(image) {
		return "", errors.New("source image identity is unavailable")
	}
	return image, nil
}
func (e DockerExecutor) validateBackupVolume(ctx context.Context, item core.StorageResource) error {
	raw, err := e.backupOutput(ctx, "volume", "inspect", "--format", "{{json .}}", item.Name)
	if err != nil {
		return errors.New("owned backup storage is unavailable")
	}
	var volume struct {
		Name, CreatedAt, Driver string
		Labels                  map[string]string
	}
	if json.Unmarshal([]byte(raw), &volume) != nil || volume.Name != item.Name || volume.CreatedAt+":"+volume.Driver != item.Identity || storageEvidence(volume.Labels) != item.Evidence || volume.Labels["dispatch.project"] != item.ProjectID || volume.Labels["dispatch.service-provision"] != item.ProvisionRunID {
		return errors.New("backup storage identity or ownership changed")
	}
	return nil
}
func backupArtifactResult(r core.WorkloadBackupRequest, a workloadbackup.Artifact) core.WorkloadBackupResult {
	return core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, State: "ready", ArtifactID: a.ID, Checksum: a.Checksum, PlaintextChecksum: a.PlaintextChecksum, Bytes: a.Bytes, ImageID: a.ImageID, CleanupState: "complete", Message: "Encrypted database-native archive is available."}
}
func readBackupArtifact(dir string, r core.WorkloadBackupRequest) (workloadbackup.Artifact, error) {
	var a workloadbackup.Artifact
	if err := readBackupJSON(dir, "manifest.enc", r.Key, r.Backup.ID, &a); err != nil {
		return a, errors.New("backup manifest is inaccessible or cannot be decrypted")
	}
	if a.ID != r.Backup.ID || a.ProjectID != r.Backup.ProjectID || a.Encryption != "AES-256-GCM-chunks-v1" || !backupImageID.MatchString(a.ImageID) {
		return a, errors.New("backup manifest ownership is invalid")
	}
	digest, n, err := workloadbackup.Checksum(filepath.Join(dir, "archive.enc"))
	if err != nil {
		return a, errors.New("backup archive is inaccessible")
	}
	if digest != a.Checksum || n != a.Bytes || r.Backup.Checksum != "" && r.Backup.Checksum != a.Checksum {
		return a, errors.New("backup archive checksum does not match")
	}
	return a, nil
}
func (e DockerExecutor) RunWorkloadBackup(ctx context.Context, r core.WorkloadBackupRequest, server core.Server) (core.WorkloadBackupResult, error) {
	result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "failed", CleanupState: "complete"}
	if server.AgentNodeID != "" {
		return result, errors.New("agent-bound backups require remote execution")
	}
	if err := ValidateServiceTarget(server, "docker"); err != nil {
		return result, err
	}
	if err := ValidateWorkloadBackupRequest(r, server); err != nil {
		return result, err
	}
	dir, err := privateBackupDirectory(e.WorkloadBackupDirectory, r.Backup.ID)
	if err != nil {
		return result, err
	}
	if r.Action == "reconcile" {
		return e.reconcileWorkloadBackup(ctx, dir, r)
	}
	if r.Action != "inspect" {
		var completed core.WorkloadBackupResult
		if readBackupJSON(dir, "result-"+r.OperationID+".enc", r.Key, r.Backup.ID, &completed) == nil {
			if completed.OperationID != r.OperationID || completed.BackupID != r.Backup.ID || completed.ProjectID != r.Backup.ProjectID {
				return result, errors.New("backup operation ownership changed")
			}
			return completed, nil
		}
	}
	if r.Action == "backup" {
		result, err = e.createWorkloadBackup(ctx, dir, r, server)
		if err == nil {
			err = writeBackupJSON(dir, "result-"+r.OperationID+".enc", r.Key, r.Backup.ID, result)
		}
		return result, err
	}
	if r.Action == "delete" {
		return e.deleteWorkloadBackup(dir, r)
	}
	a, err := readBackupArtifact(dir, r)
	if err != nil {
		return result, err
	}
	result = backupArtifactResult(r, a)
	if r.Action == "inspect" {
		return result, nil
	}

	// Decrypt and authenticate every byte before opening a destination connection.
	plain, err := os.CreateTemp(dir, ".restore-")
	if err != nil {
		return result, errors.New("cannot prepare isolated restore storage")
	}
	// Keep decrypted bytes only in an unlinked private file. A process crash cannot
	// leave an unencrypted restore artifact in the retained backup directory.
	if err = os.Remove(plain.Name()); err != nil {
		plain.Close()
		return result, err
	}
	defer plain.Close()
	encrypted, err := os.Open(filepath.Join(dir, "archive.enc"))
	if err != nil {
		return result, errors.New("backup archive is inaccessible")
	}
	digest, n, err := workloadbackup.Decrypt(plain, encrypted, r.Key, r.Backup.ID)
	encrypted.Close()
	if err != nil || digest != a.PlaintextChecksum || n != a.PlaintextBytes {
		return result, errors.New("backup cannot be decrypted or its plaintext integrity check failed")
	}
	if _, err = plain.Seek(0, io.SeekStart); err != nil {
		return result, err
	}
	if r.Action == "verify" {
		return e.verifyWorkloadBackup(ctx, dir, plain, r, a)
	}
	result, err = e.restoreWorkloadBackup(ctx, plain, r, a, server)
	if err == nil {
		err = writeBackupJSON(dir, "result-"+r.OperationID+".enc", r.Key, r.Backup.ID, result)
	}
	return result, err
}
func (e DockerExecutor) createWorkloadBackup(ctx context.Context, dir string, r core.WorkloadBackupRequest, server core.Server) (core.WorkloadBackupResult, error) {
	result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "failed", CleanupState: "complete"}
	owner := workloadbackup.Artifact{ID: r.Backup.ID, ProjectID: r.Backup.ProjectID, RequestDigest: backupRequestDigest(r)}
	var previous workloadbackup.Artifact
	if err := readBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, &previous); err == nil {
		if previous.ID != owner.ID || previous.ProjectID != owner.ProjectID || previous.RequestDigest != owner.RequestDigest {
			return result, errors.New("backup artifact already belongs to a different accepted source")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, errors.New("backup artifact ownership cannot be verified")
	} else if err = writeBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, owner); err != nil {
		return result, err
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.enc")); err == nil {
		a, err := readBackupArtifact(dir, r)
		if err == nil && a.RequestDigest != owner.RequestDigest {
			err = errors.New("backup artifact source changed")
		}
		return backupArtifactResult(r, a), err
	}
	image, err := e.validateBackupSource(ctx, r, server)
	if err != nil {
		return result, err
	}
	vars, err := provisionVariables(r.Source, "")
	if err != nil {
		return result, err
	}
	f, err := os.CreateTemp(dir, ".archive-")
	if err != nil {
		return result, errors.New("cannot create encrypted backup archive")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	reader, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := e.backupCommand(ctx, nil, writer, "exec", "--user", "postgres", r.Backup.SourceResourceID, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--username="+vars["service.username"], "--dbname="+vars["service.database"])
		writer.CloseWithError(err)
		done <- err
	}()
	plainDigest, plainBytes, err := workloadbackup.Encrypt(f, reader, r.Key, r.Backup.ID)
	reader.CloseWithError(err)
	commandErr := <-done
	if err != nil || commandErr != nil {
		return result, errors.New("database-native backup failed; inspect database access and target storage space")
	}
	if err = f.Sync(); err != nil {
		return result, errors.New("encrypted backup could not be persisted")
	}
	if err = f.Close(); err != nil {
		return result, err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, "archive.enc")); err != nil {
		return result, err
	}
	checksum, n, err := workloadbackup.Checksum(filepath.Join(dir, "archive.enc"))
	if err != nil {
		return result, err
	}
	a := workloadbackup.Artifact{ID: r.Backup.ID, ProjectID: r.Backup.ProjectID, Checksum: checksum, PlaintextChecksum: plainDigest, Bytes: n, PlaintextBytes: plainBytes, ImageID: image, Encryption: "AES-256-GCM-chunks-v1"}
	a.RequestDigest = backupRequestDigest(r)
	if err = writeBackupJSON(dir, "manifest.enc", r.Key, r.Backup.ID, a); err != nil {
		return result, errors.New("encrypted archive exists but its manifest could not be committed")
	}
	return backupArtifactResult(r, a), nil
}
func (e DockerExecutor) restoreInto(ctx context.Context, archive io.Reader, container, username, database string, checks []core.BackupIntegrityCheck) error {
	args := []string{"exec", "-i", "--user", "postgres", "-e", "PGOPTIONS=-c transaction_timeout=1500000 -c statement_timeout=1500000", container, "pg_restore", "--clean", "--if-exists", "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", "--username=" + username, "--dbname=" + database}
	if err := e.backupCommand(ctx, archive, io.Discard, args...); err != nil {
		return errors.New("database restore failed or was interrupted; inspect its recorded operation before another restore")
	}
	for _, check := range checks {
		got, err := e.backupOutput(ctx, "exec", "--user", "postgres", container, "psql", "-X", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--username="+username, "--dbname="+database, "-tA", "--command=BEGIN READ ONLY; "+check.Query+"; COMMIT;")
		if err != nil {
			return errors.New("restored database failed a configured integrity query")
		}
		got = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(got, "BEGIN\n"), "\nCOMMIT"))
		if got != strings.TrimSpace(check.Expected) {
			return errors.New("restored database failed a configured integrity assertion")
		}
	}
	return nil
}
func (e DockerExecutor) restoreWorkloadBackup(ctx context.Context, archive io.Reader, r core.WorkloadBackupRequest, a workloadbackup.Artifact, server core.Server) (core.WorkloadBackupResult, error) {
	result := backupArtifactResult(r, a)
	result.State = "unknown"
	dest, err := e.InspectServiceResource(ctx, *r.Destination, server)
	if err != nil || dest.State != "ready" || dest.ResourceID != r.ExpectedDestination {
		return result, errors.New("reviewed restore destination changed")
	}
	if err = e.validateBackupVolume(ctx, *r.DestinationStorage); err != nil {
		return result, err
	}
	vars, err := provisionVariables(*r.Destination, "")
	if err != nil {
		return result, err
	}
	if err = e.restoreInto(ctx, archive, dest.ResourceID, vars["service.username"], vars["service.database"], r.Checks); err != nil {
		return result, err
	}
	result.State, result.Message = "restored", "Backup objects restored in one transaction and configured integrity checks passed."
	return result, nil
}
func (e DockerExecutor) verifyWorkloadBackup(ctx context.Context, dir string, archive io.Reader, r core.WorkloadBackupRequest, a workloadbackup.Artifact) (result core.WorkloadBackupResult, err error) {
	result = backupArtifactResult(r, a)
	result.State = "failed"
	result.CleanupState = "pending"
	name := "dispatch-backup-verify-" + strings.ToLower(r.OperationID)
	if len(name) > 200 {
		return result, errors.New("verification operation identity is too long")
	}
	var container string
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cleanupErr := e.cleanupBackupVerification(cleanup, name, r)
		if cleanupErr != nil {
			result.CleanupState = "failed"
			result.State = "unknown"
			err = errors.New("isolated verification cleanup failed; reconcile the temporary resources before another verification")
		} else {
			result.CleanupState = "complete"
		}
		_ = writeBackupJSON(dir, "verification-"+r.OperationID+".enc", r.Key, r.Backup.ID, result)
	}()
	// Reusing a temporary name is forbidden until its interrupted operation is inspected.
	existing, e1 := e.backupOutput(ctx, "ps", "-aq", "--no-trunc", "--filter", "name=^/"+name+"$")
	if e1 != nil || existing != "" {
		return result, errors.New("verification resources already exist; reconcile their original operation")
	}
	oldVolume, volumeErr := e.backupOutput(ctx, "volume", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if volumeErr != nil || oldVolume != "" {
		return result, errors.New("verification storage already exists; reconcile its original operation")
	}
	labels := []string{"--label", "dispatch.managed-by=dispatch", "--label", "dispatch.project=" + r.Backup.ProjectID, "--label", "dispatch.workload-backup=" + r.Backup.ID, "--label", "dispatch.backup-operation=" + r.OperationID}
	if err = e.backupCommand(ctx, nil, io.Discard, append(append([]string{"volume", "create"}, labels...), name)...); err != nil {
		return result, errors.New("cannot allocate isolated verification storage")
	}
	args := append([]string{"run", "-d", "--name", name, "--network", "none", "--memory", "2g", "--cpus", "1", "--security-opt", "no-new-privileges", "--env", "POSTGRES_HOST_AUTH_METHOD=trust", "--env", "POSTGRES_DB=verification", "--env", "PGDATA=/var/lib/postgresql/data/pgdata", "--mount", "type=volume,source=" + name + ",target=/var/lib/postgresql/data"}, labels...)
	args = append(args, a.ImageID)
	container, err = e.backupOutput(ctx, args...)
	if err != nil {
		return result, errors.New("cannot start isolated PostgreSQL verification")
	}
	ready := false
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for !ready {
		if e.backupCommand(ctx, nil, io.Discard, "exec", container, "pg_isready", "-U", "postgres", "-d", "verification") == nil {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-deadline.C:
			return result, errors.New("isolated verification database did not become ready")
		case <-ticker.C:
		}
	}
	if err = e.restoreInto(ctx, archive, container, "postgres", "verification", r.Checks); err != nil {
		return result, err
	}
	result.State, result.Message = "verified", "Archive authenticated, restored into isolated storage, and configured integrity checks passed."
	return result, nil
}
func (e DockerExecutor) cleanupBackupVerification(ctx context.Context, name string, r core.WorkloadBackupRequest) error {
	raw, err := e.backupOutput(ctx, "ps", "-aq", "--no-trunc", "--filter", "name=^/"+name+"$")
	if err != nil {
		return err
	}
	if raw != "" {
		if len(strings.Fields(raw)) != 1 {
			return errors.New("ambiguous verification container")
		}
		labels, e1 := e.backupOutput(ctx, "inspect", "--format", "{{json .Config.Labels}}", raw)
		var saved map[string]string
		if e1 != nil || json.Unmarshal([]byte(labels), &saved) != nil || saved["dispatch.workload-backup"] != r.Backup.ID || saved["dispatch.backup-operation"] != r.OperationID || saved["dispatch.project"] != r.Backup.ProjectID {
			return errors.New("verification container ownership changed")
		}
		if err = e.backupCommand(ctx, nil, io.Discard, "rm", "-f", raw); err != nil {
			return err
		}
	}
	names, err := e.backupOutput(ctx, "volume", "ls", "--filter", "name=^"+name+"$", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	if names == "" {
		return nil
	}
	labels, err := e.backupOutput(ctx, "volume", "inspect", "--format", "{{json .Labels}}", name)
	var saved map[string]string
	if err != nil || json.Unmarshal([]byte(labels), &saved) != nil || saved["dispatch.workload-backup"] != r.Backup.ID || saved["dispatch.backup-operation"] != r.OperationID || saved["dispatch.project"] != r.Backup.ProjectID {
		return errors.New("verification storage ownership changed")
	}
	return e.backupCommand(ctx, nil, io.Discard, "volume", "rm", name)
}

func backupRequestDigest(r core.WorkloadBackupRequest) string {
	raw, _ := json.Marshal(struct {
		Source  core.ServiceProvisionRequest
		Storage core.StorageResource
	}{r.Source, r.Storage})
	sum := sha256.Sum256(raw)
	clear(raw)
	return hex.EncodeToString(sum[:])
}
func (e DockerExecutor) deleteWorkloadBackup(dir string, r core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "unknown", CleanupState: "complete"}
	var owner workloadbackup.Artifact
	if err := readBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, &owner); err != nil || owner.ID != r.Backup.ID || owner.ProjectID != r.Backup.ProjectID || owner.RequestDigest != backupRequestDigest(r) {
		return result, errors.New("retained backup ownership cannot be verified")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.Name() == "archive.enc" || strings.HasPrefix(entry.Name(), ".archive-") {
			if entry.IsDir() {
				return result, errors.New("unexpected backup storage layout")
			}
			if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return result, errors.New("retained archive cleanup failed")
			}
		}
	}
	result.State, result.Message = "deleted", "Retained backup bytes deleted after explicit review; encrypted operation history remains."
	if err = writeBackupJSON(dir, "deleted.enc", r.Key, r.Backup.ID, result); err != nil {
		return result, err
	}
	return result, writeBackupJSON(dir, "result-"+r.OperationID+".enc", r.Key, r.Backup.ID, result)
}
func (e DockerExecutor) reconcileWorkloadBackup(ctx context.Context, dir string, r core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "unresolved", CleanupState: "complete", Message: "The previous outcome is uncertain. Inspect the destination before a fresh reviewed restore."}
	var saved core.WorkloadBackupResult
	if readBackupJSON(dir, "result-"+r.OperationID+".enc", r.Key, r.Backup.ID, &saved) == nil {
		return saved, nil
	}
	switch r.RecoveryAction {
	case "backup":
		a, err := readBackupArtifact(dir, r)
		if err != nil {
			return result, err
		}
		return backupArtifactResult(r, a), nil
	case "delete":
		if readBackupJSON(dir, "deleted.enc", r.Key, r.Backup.ID, &saved) == nil {
			return saved, nil
		}
		return result, nil
	case "verify":
		if err := e.cleanupBackupVerification(ctx, "dispatch-backup-verify-"+strings.ToLower(r.OperationID), r); err != nil {
			result.CleanupState = "failed"
			return result, err
		}
		if readBackupJSON(dir, "verification-"+r.OperationID+".enc", r.Key, r.Backup.ID, &saved) == nil && saved.State == "verified" {
			saved.CleanupState = "complete"
			return saved, nil
		}
		result.State, result.Message = "failed", "Interrupted verification resources were removed. Start a new isolated verification."
	}
	return result, nil
}

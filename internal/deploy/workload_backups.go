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
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
)

var backupDatabaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$-]{0,62}$`)

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
	if _, err := backupDatabaseVariables(r.Source); err != nil {
		return err
	}
	if r.Destination != nil {
		if _, err := backupDatabaseVariables(*r.Destination); err != nil {
			return err
		}
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
	if a.ID != r.Backup.ID || a.ProjectID != r.Backup.ProjectID || a.Encryption != "AES-256-GCM-chunks-v1" || a.RequestDigest != backupRequestDigest(r) || !backupImageID.MatchString(a.ImageID) {
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
		completed, receiptErr := readBackupOperationResult(dir, "result-"+r.OperationID+".enc", r)
		if receiptErr == nil {
			return completed, nil
		}
		if !errors.Is(receiptErr, os.ErrNotExist) {
			result.State = "unknown"
			return result, receiptErr
		}
	}
	if r.Action == "backup" {
		result, err = e.createWorkloadBackup(ctx, dir, r, server)
		if err == nil {
			err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, result)
			if err != nil {
				result.State = "unknown"
			}
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
		err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, result)
		if err != nil {
			result.State = "unknown"
		}
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
	vars, err := backupDatabaseVariables(r.Source)
	if err != nil {
		return result, err
	}
	if err = e.backupPostgresVersion(ctx, r.Backup.SourceResourceID, vars["service.username"], vars["service.database"]); err != nil {
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
		options, err := backupPostgresOptions(ctx)
		if err == nil {
			err = e.backupCommand(ctx, nil, writer, "exec", "--user", "postgres", "-e", options, r.Backup.SourceResourceID, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--username="+vars["service.username"], "--dbname="+vars["service.database"])
		}
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
	options, err := backupPostgresOptions(ctx)
	if err != nil {
		return err
	}
	args := []string{"exec", "-i", "--user", "postgres", "-e", options, container, "pg_restore", "--clean", "--if-exists", "--single-transaction", "--exit-on-error", "--no-owner", "--no-acl", "--username=" + username, "--dbname=" + database}
	if err := e.backupCommand(ctx, archive, io.Discard, args...); err != nil {
		return errors.New("database restore failed or was interrupted; inspect its recorded operation before another restore")
	}
	for _, check := range checks {
		options, err := backupPostgresOptions(ctx)
		if err != nil {
			return err
		}
		got, err := e.backupOutput(ctx, "exec", "--user", "postgres", "-e", options, container, "psql", "-X", "--no-psqlrc", "--set=ON_ERROR_STOP=1", "--username="+username, "--dbname="+database, "-tA", "--command=BEGIN READ ONLY; "+check.Query+"; COMMIT;")
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
	vars, err := backupDatabaseVariables(*r.Destination)
	if err != nil {
		return result, err
	}
	if err = e.backupPostgresVersion(ctx, dest.ResourceID, vars["service.username"], vars["service.database"]); err != nil {
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
		if journalErr := writeBackupOperationResult(dir, "verification-"+r.OperationID+".enc", r, result); journalErr != nil {
			result.State = "unknown"
			err = errors.New("verification outcome could not be persisted; reconcile the original operation")
		} else if result.CleanupState == "complete" {
			if journalErr = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, result); journalErr != nil {
				result.State = "unknown"
				err = errors.New("verification outcome could not be persisted; reconcile the original operation")
			}
		}
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
		if e.backupCommand(ctx, nil, io.Discard, "exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "verification") == nil {
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
	ownerErr := readBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, &owner)
	if errors.Is(ownerErr, os.ErrNotExist) {
		// A failure before the initial owner record commits may leave an empty
		// accepted backup directory. Explicit deletion can retire that empty record.
		if backupHasNoArchiveData(dir) {
			owner = workloadbackup.Artifact{ID: r.Backup.ID, ProjectID: r.Backup.ProjectID, RequestDigest: backupRequestDigest(r)}
			ownerErr = writeBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, owner)
		}
	}
	if ownerErr != nil || owner.ID != r.Backup.ID || owner.ProjectID != r.Backup.ProjectID || owner.RequestDigest != backupRequestDigest(r) {
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
		result.State = "unknown"
		return result, err
	}
	err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, result)
	if err != nil {
		result.State = "unknown"
	}
	return result, err
}
func (e DockerExecutor) reconcileWorkloadBackup(ctx context.Context, dir string, r core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "unresolved", CleanupState: "complete", Message: "The previous outcome is uncertain. Inspect the destination before a fresh reviewed restore."}
	saved, receiptErr := readBackupOperationResult(dir, "result-"+r.OperationID+".enc", r)
	if receiptErr == nil {
		return saved, nil
	}
	if !errors.Is(receiptErr, os.ErrNotExist) {
		return result, receiptErr
	}
	switch r.RecoveryAction {
	case "backup":
		a, err := readBackupArtifact(dir, r)
		if err != nil {
			var owner workloadbackup.Artifact
			ownershipErr := readBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, &owner)
			if errors.Is(ownershipErr, os.ErrNotExist) && backupHasNoArchiveData(dir) {
				result.State, result.Message = "failed", "The accepted backup did not create archive bytes. Retire this empty record after review."
				return result, nil
			}
			if ownershipErr != nil || owner.ID != r.Backup.ID || owner.ProjectID != r.Backup.ProjectID || owner.RequestDigest != backupRequestDigest(r) {
				return result, errors.New("backup ownership cannot be verified; recover its encryption key before retiring the archive")
			}
			result.State, result.Message = "failed", "Owned backup archive or manifest failed integrity verification. Review and explicitly delete the failed archive before replacing it."
			return result, nil
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
		if saved, receiptErr = readBackupOperationResult(dir, "verification-"+r.OperationID+".enc", r); receiptErr == nil && saved.State == "verified" {
			saved.CleanupState = "complete"
			return saved, nil
		}
		result.State, result.Message = "failed", "Interrupted verification resources were removed. Start a new isolated verification."
	}
	return result, nil
}

func (e DockerExecutor) backupPostgresVersion(ctx context.Context, container, user, database string) error {
	options, err := backupPostgresOptions(ctx)
	if err != nil {
		return err
	}
	version, err := e.backupOutput(ctx, "exec", "--user", "postgres", "-e", options, container, "psql", "-X", "--no-psqlrc", "--username="+user, "--dbname="+database, "-tA", "--command=SHOW server_version_num")
	n, parseErr := strconv.Atoi(version)
	if err != nil || parseErr != nil || n < 170000 {
		return errors.New("native backup recovery requires PostgreSQL 17 or newer with bounded transaction execution")
	}
	return nil
}

func backupDatabaseVariables(req core.ServiceProvisionRequest) (map[string]string, error) {
	values, err := provisionVariables(req, "")
	if err != nil {
		return nil, err
	}
	if !backupDatabaseName.MatchString(values["service.database"]) || !backupDatabaseName.MatchString(values["service.username"]) {
		return nil, errors.New("native backup database and role names must be simple identifiers; connection strings are forbidden")
	}
	return values, nil
}

// The journal binds completion to immutable accepted inputs, even for a direct
// executor caller. Mutable inventory status must not change legitimate replay.
type backupOperationReceipt struct {
	RequestDigest string                    `json:"requestDigest"`
	Result        core.WorkloadBackupResult `json:"result"`
}

func backupOperationDigest(r core.WorkloadBackupRequest) string {
	b := r.Backup
	r.Backup = core.WorkloadBackup{ID: b.ID, ProjectID: b.ProjectID, ServerID: b.ServerID, NodeID: b.NodeID, SourceRunID: b.SourceRunID, StorageID: b.StorageID, SourceResourceID: b.SourceResourceID, ArtifactID: b.ArtifactID, Consistency: b.Consistency, Format: b.Format, Location: b.Location, Policy: b.Policy}
	if r.Action == "reconcile" {
		r.Action = r.RecoveryAction
	}
	r.RecoveryAction, r.Key = "", ""
	raw, _ := json.Marshal(r)
	sum := sha256.Sum256(raw)
	clear(raw)
	return hex.EncodeToString(sum[:])
}
func writeBackupOperationResult(dir, name string, r core.WorkloadBackupRequest, result core.WorkloadBackupResult) error {
	return writeBackupJSON(dir, name, r.Key, r.Backup.ID, backupOperationReceipt{RequestDigest: backupOperationDigest(r), Result: result})
}
func readBackupOperationResult(dir, name string, r core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	var receipt backupOperationReceipt
	if err := readBackupJSON(dir, name, r.Key, r.Backup.ID, &receipt); err != nil {
		return receipt.Result, err
	}
	result := receipt.Result
	if receipt.RequestDigest != backupOperationDigest(r) || result.OperationID != r.OperationID || result.BackupID != r.Backup.ID || result.ProjectID != r.Backup.ProjectID {
		return core.WorkloadBackupResult{}, errors.New("backup operation receipt does not match the accepted action or destination")
	}
	return result, nil
}

// Empty directories and unfinished encrypted metadata writes contain no archive.
// Any possible archive or other file requires its valid ownership manifest.
func backupHasNoArchiveData(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !strings.HasPrefix(entry.Name(), ".metadata-") || !info.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// Docker client cancellation does not prove the exec process stopped. Bound each
// database launch by the remaining controller deadline, including a margin,
// rather than granting a new 25-minute transaction after archive preparation.
func backupPostgresOptions(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	budget := 25 * time.Minute
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		margin := min(time.Second, remaining/10)
		budget = min(budget, remaining-margin)
	}
	millis := budget.Milliseconds()
	if millis < 1 {
		return "", context.DeadlineExceeded
	}
	value := strconv.FormatInt(millis, 10)
	return "PGOPTIONS=-c transaction_timeout=" + value + " -c statement_timeout=" + value, nil
}

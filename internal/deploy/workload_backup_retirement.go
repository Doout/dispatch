package deploy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
)

func intactOffsite(r core.WorkloadBackupRequest) bool {
	off, a := r.Backup.Offsite, r.OffsiteAccess
	return off != nil && off.DeletedAt == nil && a != nil && off.StoreID == a.StoreID && off.ArchiveKey == a.Archive.Key && off.ManifestKey == a.Manifest.Key && off.ManifestChecksum != "" && r.Backup.Checksum != ""
}

// DeleteOffsiteWorkloadBackup runs in the controller. Removing the source target
// does not remove the controller's encrypted key, exact keys or operation lease.
func DeleteOffsiteWorkloadBackup(ctx context.Context, client *http.Client, r core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	out := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "unknown", CleanupState: "pending"}
	if !intactOffsite(r) {
		return out, errors.New("reviewed offsite object identity changed")
	}
	for _, item := range []struct {
		access   backupstore.ObjectAccess
		checksum string
		max      int64
	}{{r.OffsiteAccess.Archive, r.Backup.Checksum, r.OffsiteAccess.MaxBytes}, {r.OffsiteAccess.Manifest, r.Backup.Offsite.ManifestChecksum, 1 << 20}} {
		if err := backupstore.Delete(ctx, client, item.access, r.Backup.ProjectID, r.Backup.ID, r.OffsiteAccess.StoreID, item.checksum, item.max); err != nil {
			return out, err
		}
	}
	out.State, out.CleanupState, out.Message = "offsite-deleted", "complete", "The reviewed encrypted archive and manifest are absent. Controller recovery metadata and history remain."
	return out, nil
}

func (e DockerExecutor) retireLocalWorkloadBackup(ctx context.Context, dir string, r core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	out := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "failed", CleanupState: "complete"}
	off := r.Backup.Offsite
	if !intactOffsite(r) || off.VerifiedAt == nil || off.VerificationState != "verified" {
		return out, errors.New("independently verified offsite recovery point is required")
	}
	var owner workloadbackup.Artifact
	if err := readBackupJSON(dir, "owner.enc", r.Key, r.Backup.ID, &owner); err != nil || owner.ID != r.Backup.ID || owner.ProjectID != r.Backup.ProjectID || owner.RequestDigest != backupRequestDigest(r) {
		return out, errors.New("local backup ownership cannot be verified")
	}
	// Use a fresh directory. A surviving local manifest or archive must not mask a
	// missing, changed or unauthenticated remote recovery point.
	fresh, err := os.MkdirTemp(dir, ".retirement-verification-")
	if err != nil {
		return out, err
	}
	defer os.RemoveAll(fresh)
	inspect := r
	inspect.Action = "verify"
	if err = e.downloadWorkloadBackup(ctx, fresh, inspect); err != nil {
		return out, err
	}
	artifact, err := readBackupArtifact(fresh, inspect)
	if err != nil {
		return out, err
	}
	archive, err := os.Open(filepath.Join(fresh, "archive.enc"))
	if err != nil {
		return out, err
	}
	plain, size, err := workloadbackup.Decrypt(io.Discard, archive, r.Key, r.Backup.ID)
	archive.Close()
	if err != nil || plain != artifact.PlaintextChecksum || size != artifact.PlaintextBytes {
		return out, errors.New("offsite archive plaintext authentication failed")
	}
	if err = os.RemoveAll(fresh); err != nil {
		out.State, out.CleanupState = "unknown", "pending"
		return out, errors.New("temporary verification bytes require cleanup before retirement")
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if r.Action == "reconcile" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			out.State, out.CleanupState = "unresolved", "pending"
			return out, err
		}
		for _, entry := range entries {
			if entry.Name() == "archive.enc" || strings.HasPrefix(entry.Name(), ".archive-") || strings.HasPrefix(entry.Name(), ".retirement-verification-") {
				out.State, out.CleanupState, out.Message = "failed", "complete", "The original retirement left local archive or staging bytes. Request a fresh reviewed retirement."
				return out, nil
			}
		}

		// Reconciliation confirms absence and preservation; it never resumes
		// deleting archive bytes from a job with an uncertain outcome.
		out.State, out.CleanupState, out.Message = "local-retired", "complete", "Original local bytes are absent and the independently verified offsite archive remains authenticated."
		if err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, out); err != nil {
			out.State, out.CleanupState = "unknown", "pending"
		}
		return out, err
	}
	out.State, out.CleanupState = "unknown", "pending"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out, err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".retirement-verification-") {
			if err = cleanupRetirementVerificationStaging(filepath.Join(dir, entry.Name())); err != nil {
				return out, err
			}
		}
		if entry.Name() == "archive.enc" || strings.HasPrefix(entry.Name(), ".archive-") {
			info, e1 := entry.Info()
			if e1 != nil || !info.Mode().IsRegular() {
				return out, errors.New("unexpected local backup storage layout")
			}
			if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				return out, errors.New("local backup retirement outcome is unresolved")
			}
		}
	}
	// Sync the directory before advertising that server protection may be released.
	folder, err := os.Open(dir)
	if err == nil {
		err = folder.Sync()
		folder.Close()
	}
	if err != nil {
		return out, err
	}
	out.State, out.CleanupState, out.Message = "local-retired", "complete", "Local archive bytes are retired. Independently verified offsite recovery metadata and encrypted controller key remain."
	if err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, out); err != nil {
		out.State, out.CleanupState = "unknown", "pending"
		return out, err
	}
	return out, nil
}

func cleanupRetirementVerificationStaging(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("unexpected retirement staging layout")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || (name != "archive.enc" && name != "offsite-manifest.enc" && !strings.HasPrefix(name, ".offsite-")) {
			return errors.New("unexpected retirement staging contents")
		}
	}
	for _, entry := range entries {
		if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return errors.New("retirement staging cleanup is unresolved")
		}
	}
	return os.Remove(dir)
}

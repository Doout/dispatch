package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

var backupImageReference = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

func offsiteRecovery(r core.WorkloadBackupRequest, server core.Server) bool {
	action := r.Action
	if action == "reconcile" {
		action = r.RecoveryAction
	}
	return (action == "restore" || action == "verify") && r.Backup.Offsite != nil && r.OffsiteAccess != nil && r.OffsiteAccess.StoreID == r.Backup.Offsite.StoreID && r.OffsiteAccess.Archive.Key == r.Backup.Offsite.ArchiveKey && r.OffsiteAccess.Manifest.Key == r.Backup.Offsite.ManifestKey && r.Destination != nil && r.DestinationStorage != nil && r.Destination.Run.Target != nil && r.Destination.Run.Target.ServerID == server.ID && r.Destination.Run.ProjectID == r.Backup.ProjectID && r.DestinationStorage.ServerID == server.ID && r.DestinationStorage.ProjectID == r.Backup.ProjectID && r.DestinationStorage.ProvisionRunID == r.Destination.Run.ID && r.DestinationStorage.Ownership == "verified" && r.ExpectedDestination != ""
}
func (e DockerExecutor) exportWorkloadBackup(ctx context.Context, dir string, r core.WorkloadBackupRequest, inspect bool) (core.WorkloadBackupResult, error) {
	result := core.WorkloadBackupResult{BackupID: r.Backup.ID, ProjectID: r.Backup.ProjectID, OperationID: r.OperationID, ArtifactID: r.Backup.ID, State: "failed", CleanupState: "complete"}
	access := r.OffsiteAccess
	if access == nil {
		return result, errors.New("scoped offsite access is missing")
	}
	a, err := readBackupArtifact(dir, r)
	if err != nil {
		return result, err
	}
	path := filepath.Join(dir, "offsite-manifest.enc")
	var exported workloadbackup.Artifact
	err = readBackupJSON(dir, "offsite-manifest.enc", r.Key, r.Backup.ID, &exported)
	if errors.Is(err, os.ErrNotExist) && !inspect {
		raw, e1 := e.backupOutput(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", a.ImageID)
		var refs []string
		if e1 != nil || json.Unmarshal([]byte(raw), &refs) != nil {
			return result, errors.New("source image has no pinned pull reference")
		}
		for _, ref := range refs {
			if backupImageReference.MatchString(ref) {
				a.ImageReference = ref
				break
			}
		}
		if a.ImageReference == "" {
			return result, errors.New("source image has no pinned pull reference")
		}
		err = writeBackupJSON(dir, "offsite-manifest.enc", r.Key, r.Backup.ID, a)
		exported = a
	}
	if err != nil || exported.Checksum != a.Checksum || exported.RequestDigest != a.RequestDigest || !backupImageReference.MatchString(exported.ImageReference) {
		return result, errors.New("offsite manifest identity is invalid")
	}
	if inspect {
		for _, object := range []backupstore.ObjectAccess{access.Archive, access.Manifest} {
			_, exists, e1 := backupstore.Head(ctx, e.BackupObjectClient, object, r.Backup.ProjectID, r.Backup.ID, access.StoreID)
			if e1 != nil {
				result.State = "unresolved"
				return result, e1
			}
			if !exists {
				result.Message = "Export is incomplete; a fresh keyed export can safely complete the original create-only objects."
				return result, nil
			}
		}
	}
	var manifestDigest string
	if !inspect {
		result.State = "unknown"
	}
	for _, item := range []struct {
		access backupstore.ObjectAccess
		path   string
		max    int64
	}{{access.Archive, filepath.Join(dir, "archive.enc"), access.MaxBytes}, {access.Manifest, path, 1 << 20}} {
		local, _, e1 := workloadbackup.Checksum(item.path)
		if e1 != nil {
			return result, e1
		}
		var actual string
		if inspect {
			actual, _, err = backupstore.Download(ctx, e.BackupObjectClient, item.access, io.Discard, r.Backup.ProjectID, r.Backup.ID, access.StoreID, item.max)
		} else {
			actual, _, err = backupstore.Upload(ctx, e.BackupObjectClient, item.access, item.path, r.Backup.ProjectID, r.Backup.ID, access.StoreID, item.max)
		}
		if err != nil || actual != local {
			return result, errors.New("offsite export is incomplete or its immutable bytes differ")
		}
		if item.path == path {
			manifestDigest = actual
		}
	}
	result = backupArtifactResult(r, a)
	result.State = "exported"
	result.Message = "Encrypted archive and manifest are confirmed in the offsite store."
	result.Offsite = &core.BackupOffsiteArtifact{StoreID: access.StoreID, ArchiveKey: access.Archive.Key, ManifestKey: access.Manifest.Key, ManifestChecksum: manifestDigest, ImageReference: exported.ImageReference, ConfirmedAt: time.Now().UTC()}
	if !inspect {
		if err = writeBackupOperationResult(dir, "result-"+r.OperationID+".enc", r, result); err != nil {
			result.State = "unknown"
		}
	}
	return result, err
}
func (e DockerExecutor) downloadWorkloadBackup(ctx context.Context, dir string, r core.WorkloadBackupRequest) error {
	off := r.Backup.Offsite
	access := r.OffsiteAccess
	if off == nil || access == nil || off.StoreID != access.StoreID || off.ArchiveKey != access.Archive.Key || off.ManifestKey != access.Manifest.Key {
		return errors.New("offsite archive identity is invalid")
	}
	for _, item := range []struct {
		name, digest string
		access       backupstore.ObjectAccess
		max          int64
	}{{"offsite-manifest.enc", off.ManifestChecksum, access.Manifest, 1 << 20}, {"archive.enc", r.Backup.Checksum, access.Archive, access.MaxBytes}} {
		file, err := os.CreateTemp(dir, ".offsite-")
		if err != nil {
			return err
		}
		name := file.Name()
		digest, _, err := backupstore.Download(ctx, e.BackupObjectClient, item.access, file, r.Backup.ProjectID, r.Backup.ID, access.StoreID, item.max)
		if err == nil && digest != item.digest {
			err = errors.New("offsite archive checksum changed")
		}
		if err == nil {
			err = file.Sync()
		}
		file.Close()
		if err == nil {
			err = os.Rename(name, filepath.Join(dir, item.name))
		}
		os.Remove(name)
		if err != nil {
			return err
		}
	}
	a, err := readBackupArtifact(dir, r)
	if err != nil {
		return err
	}
	if a.ImageReference != off.ImageReference {
		return errors.New("offsite pinned image identity changed")
	}
	return nil
}

package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
)

func TestLocalRetirementIndependentOffsiteAuthenticationAndRecovery(t *testing.T) {
	r := backupUnitRequest(t)
	r.Backup.ImageID = "sha256:" + strings.Repeat("a", 64)
	root := filepath.Join(t.TempDir(), "backups")
	dir, err := privateBackupDirectory(root, r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("recoverable encrypted database fixture")
	file, err := os.Create(filepath.Join(dir, "archive.enc"))
	if err != nil {
		t.Fatal(err)
	}
	plain, plainBytes, err := workloadbackup.Encrypt(file, bytes.NewReader(plaintext), r.Key, r.Backup.ID)
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	checksum, size, err := workloadbackup.Checksum(filepath.Join(dir, "archive.enc"))
	if err != nil {
		t.Fatal(err)
	}
	r.Backup.Checksum, r.Backup.Bytes = checksum, size
	artifact := workloadbackup.Artifact{ID: r.Backup.ID, ProjectID: r.Backup.ProjectID, Checksum: checksum, Bytes: size, PlaintextChecksum: plain, PlaintextBytes: plainBytes, ImageID: r.Backup.ImageID, RequestDigest: backupRequestDigest(r), Encryption: "AES-256-GCM-chunks-v1"}
	for _, name := range []string{"manifest.enc", "owner.enc"} {
		if err = writeBackupJSON(dir, name, r.Key, r.Backup.ID, artifact); err != nil {
			t.Fatal(err)
		}
	}
	server, access := backupObjectFixture(t, r.Backup.ProjectID, r.Backup.ID)
	r.Action, r.OffsiteAccess = "export", &access
	executor := DockerExecutor{WorkloadBackupDirectory: root, BackupObjectClient: server.Client(), run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		if strings.Join(args, " ") == "image inspect --format {{json .RepoDigests}} "+r.Backup.ImageID {
			io.WriteString(out, `["docker.io/library/postgres@sha256:`+strings.Repeat("b", 64)+`"]`)
			return nil
		}
		return errors.New("retirement must not access Docker")
	}}
	exported, err := executor.exportWorkloadBackup(context.Background(), dir, r, false)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r.Backup.Offsite = exported.Offsite
	r.Backup.Offsite.VerifiedAt = &now
	r.Backup.Offsite.VerificationState = "verified"
	r.Action, r.OperationID = "retire-local", "retirement-original"
	bad := r
	off := *r.Backup.Offsite
	off.ManifestChecksum = strings.Repeat("d", 64)
	bad.Backup.Offsite = &off
	if _, err = executor.retireLocalWorkloadBackup(context.Background(), dir, bad); err == nil {
		t.Fatal("changed remote manifest was trusted")
	}
	if _, err = os.Stat(filepath.Join(dir, "archive.enc")); err != nil {
		t.Fatal("local bytes removed before preservation proof", err)
	}
	// A crashed capture may have owned staging bytes. Inspection cannot claim
	// completion while they remain, and cannot delete them during inspection.
	staged := filepath.Join(dir, ".archive-interrupted")
	if err = os.WriteFile(staged, []byte("owned encrypted staging bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	inspected := r
	inspected.Action, inspected.RecoveryAction = "reconcile", "retire-local"
	pending, err := executor.reconcileWorkloadBackup(context.Background(), dir, inspected)
	if err != nil || pending.State != "failed" {
		t.Fatal("inspection hid retained staging bytes", pending, err)
	}
	if _, err = os.Stat(staged); err != nil {
		t.Fatal("inspection deleted local staging bytes")
	}
	abandoned := filepath.Join(dir, ".retirement-verification-interrupted")
	if err = os.Mkdir(abandoned, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(abandoned, ".offsite-interrupted"), []byte("partial encrypted remote download"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := executor.RunWorkloadBackup(context.Background(), r, core.Server{ID: r.Backup.ServerID, Address: "local", Runtime: "docker"})
	if err != nil || result.State != "local-retired" {
		t.Fatal(result, err)
	}
	if _, err = os.Stat(filepath.Join(dir, "archive.enc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("local archive bytes remain", err)
	}
	if _, err = os.Stat(staged); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("staging archive bytes remain after retirement")
	}
	if _, err = os.Stat(abandoned); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("interrupted offsite verification bytes remain after retirement")
	}
	for _, name := range []string{"owner.enc", "manifest.enc", "result-retirement-original.enc"} {
		if _, err = os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal("recovery history was discarded", name, err)
		}
	}
	// Restarted direct execution returns the bound journal. Recovery inspection
	// also recognizes the exact accepted retirement without repeating a mutation.
	again, err := executor.RunWorkloadBackup(context.Background(), r, core.Server{ID: r.Backup.ServerID, Address: "local", Runtime: "docker"})
	if err != nil || again.State != "local-retired" {
		t.Fatal(again, err)
	}
	r.Action, r.RecoveryAction = "reconcile", "retire-local"
	again, err = executor.reconcileWorkloadBackup(context.Background(), dir, r)
	if err != nil || again.State != "local-retired" {
		t.Fatal(again, err)
	}
	// Remove the entire old local directory. Recovery only needs the controller's
	// retained request/key, immutable offsite identity and a fresh directory.
	if err = os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	r.Action = "verify"
	r.RecoveryAction = ""
	if err = executor.downloadWorkloadBackup(context.Background(), fresh, r); err != nil {
		t.Fatal(err)
	}
	restored, err := os.Open(filepath.Join(fresh, "archive.enc"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var decoded bytes.Buffer
	if _, _, err = workloadbackup.Decrypt(&decoded, restored, r.Key, r.Backup.ID); err != nil || !bytes.Equal(decoded.Bytes(), plaintext) {
		t.Fatal("recovery after retirement failed", err)
	}
	for _, operation := range []string{"verify", "restore"} {
		for _, target := range []string{"retired-source", "other-target"} {
			for _, outcome := range []string{"success", "failure", "reconcile", "cleanup-failure"} {
				t.Run(operation+"-"+target+"-"+outcome, func(t *testing.T) {
					request := r
					request.Action, request.RecoveryAction = operation, ""
					request.OperationID = "cache-" + operation + "-" + target + "-" + outcome
					targetServer := core.Server{ID: r.Backup.ServerID, Address: "local", Runtime: "docker"}
					request.Backup.LocalState = "retired"
					if target == "other-target" {
						request.Backup.LocalState = ""
						destination := r.Source
						destination.Run.ID = "destination"
						route := *destination.Run.Target
						route.ServerID = "destination-server"
						destination.Run.Target = &route
						storage := r.Storage
						storage.ServerID = "destination-server"
						storage.ProvisionRunID = destination.Run.ID
						request.Destination, request.DestinationStorage, request.ExpectedDestination = &destination, &storage, "owned-destination"
						targetServer.ID = "destination-server"
					}
					if request.Destination == nil && operation == "restore" {
						destination := r.Source
						destination.Run.ID = "destination"
						route := *destination.Run.Target
						destination.Run.Target = &route
						storage := r.Storage
						storage.ProvisionRunID = destination.Run.ID
						request.Destination, request.DestinationStorage, request.ExpectedDestination = &destination, &storage, "owned-destination"
					}
					if request.DestinationStorage != nil {
						request.DestinationStorage.Name = "destination-volume"
						request.DestinationStorage.Identity = "created:local"
						request.DestinationStorage.Evidence = storageEvidence(provisionLabels(*request.Destination))
					}
					cacheRoot := filepath.Join(t.TempDir(), "cache")
					cacheDir, err := privateBackupDirectory(cacheRoot, request.Backup.ID)
					if err != nil {
						t.Fatal(err)
					}
					containerExists, volumeExists := false, false
					failCacheCleanup := func() error {
						path := filepath.Join(cacheDir, "archive.enc")
						if err := os.Remove(path); err != nil {
							return err
						}
						if err := os.Mkdir(path, 0700); err != nil {
							return err
						}
						return os.WriteFile(filepath.Join(path, "unresolved-bytes"), []byte("owned temporary bytes"), 0600)
					}
					runner := DockerExecutor{WorkloadBackupDirectory: cacheRoot, BackupObjectClient: server.Client(), run: func(_ context.Context, in io.Reader, out io.Writer, _ string, args ...string) error {
						switch args[0] {
						case "pull":
							return nil
						case "ps":
							if operation == "restore" {
								io.WriteString(out, "owned-destination")
								return nil
							}
							if containerExists {
								io.WriteString(out, "owned-verifier")
							}
							return nil
						case "inspect":
							if operation == "restore" {
								return json.NewEncoder(out).Encode(map[string]any{"id": "owned-destination", "labels": provisionLabels(*request.Destination), "state": map[string]any{"Status": "running", "Health": map[string]string{"Status": "healthy"}}})
							}
							labels := map[string]string{"dispatch.project": request.Backup.ProjectID, "dispatch.workload-backup": request.Backup.ID, "dispatch.backup-operation": request.OperationID}
							raw, _ := json.Marshal(labels)
							out.Write(raw)
							return nil
						case "run":
							if outcome == "cleanup-failure" {
								if err := failCacheCleanup(); err != nil {
									return err
								}
							}
							if outcome == "failure" {
								return errors.New("cannot start verification")
							}
							containerExists = true
							io.WriteString(out, "owned-verifier")
							return nil
						case "rm":
							containerExists = false
							return nil
						case "exec":
							if in != nil {
								if operation == "restore" && outcome == "cleanup-failure" {
									if err := failCacheCleanup(); err != nil {
										return err
									}
								}
								if operation == "restore" && outcome == "failure" {
									return errors.New("database restore interrupted")
								}
								_, err := io.Copy(io.Discard, in)
								return err
							}
							if strings.Contains(strings.Join(args, " "), "SHOW server_version_num") {
								io.WriteString(out, "170000")
							}
							return nil
						case "volume":
							switch args[1] {
							case "ls":
								if volumeExists {
									io.WriteString(out, "owned-verifier-volume")
								}
							case "create":
								volumeExists = true
							case "rm":
								volumeExists = false
							case "inspect":
								if operation == "restore" {
									return json.NewEncoder(out).Encode(map[string]any{"Name": request.DestinationStorage.Name, "CreatedAt": "created", "Driver": "local", "Labels": provisionLabels(*request.Destination)})
								}
								raw, _ := json.Marshal(map[string]string{"dispatch.project": request.Backup.ProjectID, "dispatch.workload-backup": request.Backup.ID, "dispatch.backup-operation": request.OperationID})
								out.Write(raw)
							}
							return nil
						}
						return errors.New("unexpected verification command")
					}}
					if outcome == "reconcile" {
						if err = runner.downloadWorkloadBackup(context.Background(), cacheDir, request); err != nil {
							t.Fatal(err)
						}
						saved := backupArtifactResult(request, artifact)
						saved.State = map[string]string{"verify": "verified", "restore": "restored"}[operation]
						if err = writeBackupOperationResult(cacheDir, "result-"+request.OperationID+".enc", request, saved); err != nil {
							t.Fatal(err)
						}
						request.Action, request.RecoveryAction = "reconcile", operation
						// Mutable verification evidence does not change the accepted journal.
						off := *request.Backup.Offsite
						off.VerificationState = "failed"
						off.VerifiedAt = nil
						request.Backup.Offsite = &off
					}
					result, err := runner.RunWorkloadBackup(context.Background(), request, targetServer)
					if outcome == "cleanup-failure" {
						if err == nil || result.State != "unknown" || result.CleanupState != "failed" {
							t.Fatal("temporary bytes were silently left behind", result, err)
						}
						if err = os.RemoveAll(filepath.Join(cacheDir, "archive.enc")); err != nil {
							t.Fatal(err)
						}
						request.Action, request.RecoveryAction = "reconcile", operation
						result, err = runner.RunWorkloadBackup(context.Background(), request, targetServer)
					}
					if outcome == "failure" {
						if err == nil || result.CleanupState != "complete" {
							t.Fatal(result, err)
						}
					} else if err != nil || result.State != map[string]string{"verify": "verified", "restore": "restored"}[operation] || result.CleanupState != "complete" {
						t.Fatal(result, err)
					}
					if _, err = os.Stat(filepath.Join(cacheDir, "archive.enc")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("unprotected temporary archive bytes remain", err)
					}
					if containerExists || volumeExists {
						t.Fatal("verification resources remain")
					}
					if _, err = os.Stat(filepath.Join(cacheDir, "result-"+request.OperationID+".enc")); err != nil && !(operation == "restore" && outcome == "failure") {
						t.Fatal("cache cleanup removed recovery journal", err)
					}
				})
			}
		}

	}
}

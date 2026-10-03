package deploy

import (
	"bytes"
	"context"
	"fmt"
	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workloadbackup"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func backupObjectFixture(t *testing.T, project, backup string) (*httptest.Server, backupstore.Access) {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		data, exists := objects[r.URL.Path]
		switch r.Method {
		case "PUT":
			if r.Header.Get("If-None-Match") != "*" || r.Header.Get("x-amz-meta-dispatch-project") != project || r.Header.Get("x-amz-meta-dispatch-backup") != backup || r.Header.Get("x-amz-meta-dispatch-store") != "store" {
				w.WriteHeader(403)
				return
			}
			if exists {
				w.WriteHeader(412)
				return
			}
			data, _ = io.ReadAll(r.Body)
			objects[r.URL.Path] = data
			w.WriteHeader(200)
		case "HEAD", "GET":
			if !exists {
				w.WriteHeader(404)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.Header().Set("x-amz-meta-dispatch-project", project)
			w.Header().Set("x-amz-meta-dispatch-backup", backup)
			w.Header().Set("x-amz-meta-dispatch-store", "store")
			if r.Method == "GET" {
				w.Write(data)
			}
		default:
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	access, err := backupstore.Grant(backupstore.Config{Endpoint: server.URL, Bucket: "backups", Region: "us-east-1", Prefix: "workloads", MaxBytes: 1 << 30}, backupstore.Credentials{AccessKeyID: "fixture-key", SecretAccessKey: "fixture-secret"}, "store", project, backup, true, false, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return server, access
}
func TestWorkloadBackupOffsiteAuthorityAndStableGrantRefresh(t *testing.T) {
	r := backupUnitRequest(t)
	r.Action = "restore"
	r.Backup.Offsite = &core.BackupOffsiteArtifact{StoreID: "store", ArchiveKey: "archive", ManifestKey: "manifest", ManifestChecksum: strings.Repeat("a", 64)}
	destination := r.Source
	destination.Run.ID = "destination"
	target := *destination.Run.Target
	target.ServerID = "new-server"
	destination.Run.Target = &target
	r.Destination = &destination
	storage := r.Storage
	storage.ServerID = "new-server"
	storage.ProvisionRunID = "destination"
	r.DestinationStorage = &storage
	r.ExpectedDestination = "owned-container"
	r.OffsiteAccess = &backupstore.Access{StoreID: "store", MaxBytes: 1 << 30, Archive: backupstore.ObjectAccess{Key: "archive", Get: "https://store/old", Head: "https://store/old-head"}, Manifest: backupstore.ObjectAccess{Key: "manifest", Get: "https://store/old-manifest"}, ExpiresAt: time.Now()}
	if err := ValidateWorkloadBackupRequest(r, core.Server{ID: "new-server"}); err != nil {
		t.Fatal(err)
	}
	digest := backupOperationDigest(r)
	r.OffsiteAccess.Archive.Get = "https://store/refreshed"
	r.OffsiteAccess.Manifest.Get = "https://store/refreshed-manifest"
	r.OffsiteAccess.ExpiresAt = time.Now().Add(time.Hour)
	if backupOperationDigest(r) != digest {
		t.Fatal("fresh grants changed the immutable operation identity")
	}
	r.OffsiteAccess.Archive.Key = "different"
	if ValidateWorkloadBackupRequest(r, core.Server{ID: "new-server"}) == nil {
		t.Fatal("unreviewed object identity was accepted")
	}
	if backupOperationDigest(r) == digest {
		t.Fatal("different object identity did not invalidate operation proof")
	}
	r.OffsiteAccess.Archive.Key = "archive"
	r.Destination.Run.ProjectID = "foreign"
	if ValidateWorkloadBackupRequest(r, core.Server{ID: "new-server"}) == nil {
		t.Fatal("foreign destination accepted")
	}
}
func TestWorkloadBackupOffsiteUnknownRestoreIsNeverReplayed(t *testing.T) {
	r := backupUnitRequest(t)
	r.Action = "reconcile"
	r.RecoveryAction = "restore"
	calls := 0
	e := DockerExecutor{run: func(context.Context, io.Reader, io.Writer, string, ...string) error { calls++; return nil }}
	result, err := e.reconcileWorkloadBackup(context.Background(), t.TempDir(), r)
	if err != nil || result.State != "unresolved" || calls != 0 {
		t.Fatal("uncertain restore replayed", result, err, calls)
	}
}

func TestWorkloadBackupEncryptedExportFreshDirectoryRecovery(t *testing.T) {
	r := backupUnitRequest(t)
	r.Backup.ImageID = "sha256:" + strings.Repeat("a", 64)
	dir, err := privateBackupDirectory(filepath.Join(t.TempDir(), "source"), r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("independent encrypted database archive fixture")
	f, err := os.Create(filepath.Join(dir, "archive.enc"))
	if err != nil {
		t.Fatal(err)
	}
	plainChecksum, plainBytes, err := workloadbackup.Encrypt(f, bytes.NewReader(plaintext), r.Key, r.Backup.ID)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	checksum, size, err := workloadbackup.Checksum(filepath.Join(dir, "archive.enc"))
	if err != nil {
		t.Fatal(err)
	}
	r.Backup.Checksum = checksum
	r.Backup.Bytes = size
	a := workloadbackup.Artifact{ID: r.Backup.ID, ProjectID: r.Backup.ProjectID, Checksum: checksum, Bytes: size, PlaintextChecksum: plainChecksum, PlaintextBytes: plainBytes, ImageID: r.Backup.ImageID, RequestDigest: backupRequestDigest(r), Encryption: "AES-256-GCM-chunks-v1"}
	if err = writeBackupJSON(dir, "manifest.enc", r.Key, r.Backup.ID, a); err != nil {
		t.Fatal(err)
	}
	server, access := backupObjectFixture(t, r.Backup.ProjectID, r.Backup.ID)
	r.OffsiteAccess = &access
	r.Action = "export"
	e := DockerExecutor{BackupObjectClient: server.Client(), run: func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		io.WriteString(out, `["docker.io/library/postgres@sha256:`+strings.Repeat("b", 64)+`"]`)
		return nil
	}}
	exported, err := e.exportWorkloadBackup(context.Background(), dir, r, false)
	if err != nil || exported.State != "exported" {
		t.Fatal(exported, err)
	}
	r.Backup.Offsite = exported.Offsite
	r.Action = "restore"
	r.OperationID = "fresh-operation"
	fresh, err := privateBackupDirectory(filepath.Join(t.TempDir(), "destination"), r.Backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.downloadWorkloadBackup(context.Background(), fresh, r); err != nil {
		t.Fatal(err)
	}
	artifact, err := readBackupArtifact(fresh, r)
	if err != nil || artifact.ImageReference != exported.Offsite.ImageReference {
		t.Fatal("downloaded encrypted manifest could not authenticate", artifact, err)
	}
	var restored bytes.Buffer
	input, err := os.Open(filepath.Join(fresh, "archive.enc"))
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	digest, n, err := workloadbackup.Decrypt(&restored, input, r.Key, r.Backup.ID)
	if err != nil || digest != plainChecksum || n != plainBytes || !bytes.Equal(restored.Bytes(), plaintext) {
		t.Fatal("fresh-target encrypted archive recovery failed", err)
	}
	if _, err = os.Stat(filepath.Join(fresh, "manifest.enc")); !os.IsNotExist(err) {
		t.Fatal("offsite recovery replaced the native manifest envelope")
	}
	// A manifest upload can fail after the archive commits. Inspection settles
	// that partial export, and a new operation completes it without another archive PUT.
	partialServer, partialAccess := backupObjectFixture(t, r.Backup.ProjectID, r.Backup.ID)
	fixtureClient := partialServer.Client()
	transport := fixtureClient.Transport
	archivePuts := 0
	failManifest := true
	fixtureClient.Transport = backupRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method == "PUT" && strings.HasSuffix(req.URL.Path, "/archive.enc") {
			archivePuts++
		}
		if req.Method == "PUT" && strings.HasSuffix(req.URL.Path, "/manifest.enc") && failManifest {
			return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
		}
		return transport.RoundTrip(req)
	})
	e.BackupObjectClient = fixtureClient
	r.Backup.Offsite = nil
	r.Action = "export"
	r.OperationID = "partial-export"
	r.OffsiteAccess = &partialAccess
	partial, err := e.exportWorkloadBackup(context.Background(), dir, r, false)
	if err == nil || partial.State != "unknown" {
		t.Fatal("partial export lost its uncertainty", partial, err)
	}
	r.Action, r.RecoveryAction = "reconcile", "export"
	partial, err = e.exportWorkloadBackup(context.Background(), dir, r, true)
	if err != nil || partial.State != "failed" {
		t.Fatal("confirmed partial export remained permanently active", partial, err)
	}
	failManifest = false
	r.Action, r.RecoveryAction, r.OperationID = "export", "", "completed-export"
	completed, err := e.exportWorkloadBackup(context.Background(), dir, r, false)
	if err != nil || completed.State != "exported" || archivePuts != 1 {
		t.Fatal("fresh export replaced the surviving immutable archive", completed, err, archivePuts)
	}

}

type backupRoundTrip func(*http.Request) (*http.Response, error)

func (f backupRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

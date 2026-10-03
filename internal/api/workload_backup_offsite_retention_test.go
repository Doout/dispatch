package api

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestOffsiteRetentionSkipsExportedOldestWithoutStarvingLocalArchives(t *testing.T) {
	a, source := backupAPIFixture(t)
	ctx := context.Background()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	secret := core.Secret{ID: "retention-object-credential", Name: "Object signing", Type: core.SecretTypeJSON, EncryptedValue: "unused-fixture", CreatedAt: now, UpdatedAt: now}
	check(a.store.CreateSecret(ctx, secret))
	objectStore := core.BackupObjectStore{ID: "retention-object-store", ProjectID: source.ProjectID, Name: "Retained exports", CredentialSecretID: secret.ID, Config: backupstore.Config{Endpoint: "https://objects.example.invalid", Bucket: "backups", Region: "us-east-1", Prefix: "retained", MaxBytes: 1024}, CreatedAt: now}
	check(a.store.(store.BackupObjectStoreStore).CreateBackupObjectStore(ctx, objectStore))
	var mu sync.Mutex
	deletes := map[string]int{}
	a.workloadBackupBackend = func(_ context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
		result := core.WorkloadBackupResult{BackupID: input.Backup.ID, ProjectID: input.Backup.ProjectID, OperationID: input.OperationID, ArtifactID: input.Backup.ID, Checksum: strings.Repeat("a", 64), Bytes: 42, CleanupState: "complete"}
		result.State = map[string]string{"backup": "ready", "verify": "verified", "delete": "deleted"}[input.Action]
		if input.Action == "delete" {
			mu.Lock()
			deletes[input.Backup.ID]++
			mu.Unlock()
		}
		return result, nil
	}
	response := mutationRequest(a, "secret", "POST", "/api/v1/workload-backup-policies", "offsite-retention-policy", map[string]any{"name": "offsite-retention", "sourceRunId": source.RunID, "intervalHours": 1, "keepLast": 1, "confirmRetention": "offsite-retention"})
	if response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	policies := a.store.(store.WorkloadBackupPolicyStore)
	backups := a.store.(store.WorkloadBackupStore)
	policy, err := policies.GetWorkloadBackupPolicy(ctx, decodeMutation(t, response).OperationID)
	check(err)
	ids := make([]string, 0, 3)
	for i := range 3 {
		policy.NextCaptureAt = now.Add(-time.Duration(3-i) * time.Minute)
		check(policies.UpdateWorkloadBackupPolicy(ctx, policy, policy.Revision))
		policy, err = policies.GetWorkloadBackupPolicy(ctx, policy.ID)
		check(err)
		check(a.acceptBackupCapture(ctx, policy))
		policy, err = policies.GetWorkloadBackupPolicy(ctx, policy.ID)
		check(err)
		awaitBackupOperation(t, a, policy.LastBackupID, "succeeded")
		b, err := backups.GetWorkloadBackup(ctx, policy.LastBackupID)
		check(err)
		verification, err := a.acceptWorkloadBackupOperation(ctx, b, "verify", ulid.Make().String(), "")
		check(err)
		a.executeWorkloadBackupOperation(verification, false)
		awaitBackupOperation(t, a, verification.ID, "succeeded")
		policy = a.refreshBackupPolicy(ctx, policy)
		ids = append(ids, b.ID)
		if i == 0 {
			b, err = backups.GetWorkloadBackup(ctx, b.ID)
			check(err)
			exported := newBackupOperation(ulid.Make().String(), b, "export")
			exported.OffsiteStoreID, exported.EncryptedInput = objectStore.ID, "encrypted-export-fixture"
			check(backups.CreateWorkloadBackupOperation(ctx, exported, b.Revision))
			exported.State = "succeeded"
			b.Offsite = &core.BackupOffsiteArtifact{StoreID: objectStore.ID, ArchiveKey: "oldest/archive.enc", ManifestKey: "oldest/manifest.enc", ManifestChecksum: strings.Repeat("b", 64), ConfirmedAt: now}
			check(backups.CompleteWorkloadBackupOperation(ctx, b, exported))
		}
	}
	if policy.State != "healthy" || policy.LastVerifiedBackupID != ids[2] {
		t.Fatal("fixture did not establish a latest verified recovery point", policy.State)
	}
	a.pruneBackupPolicy(ctx, policy)
	operations, err := backups.ListWorkloadBackupOperations(ctx, ids[1])
	check(err)
	var deletion string
	for _, operation := range operations {
		if operation.Action == "delete" {
			deletion = operation.ID
		}
	}
	if deletion == "" {
		t.Fatal("exported oldest archive starved the eligible local archive")
	}
	awaitBackupOperation(t, a, deletion, "succeeded")
	for range 3 {
		a.pruneBackupPolicy(ctx, policy)
	}
	for i, id := range ids {
		b, err := backups.GetWorkloadBackup(ctx, id)
		check(err)
		want := "ready"
		if i == 1 {
			want = "deleted"
		}
		if b.State != want || i == 0 && b.Offsite == nil || b.VerificationState != "verified" {
			t.Fatalf("archive %d lost retention evidence: state=%s verified=%s offsite=%t", i, b.State, b.VerificationState, b.Offsite != nil)
		}
		operations, err := backups.ListWorkloadBackupOperations(ctx, id)
		check(err)
		for _, operation := range operations {
			if operation.Action == "delete" && (i != 1 || operation.ID != deletion) {
				t.Fatal("retention accepted a protected or repeated deletion", i, operation.ID)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deletes) != 1 || deletes[ids[1]] != 1 {
		t.Fatal("retention sent unexpected runtime deletions", deletes)
	}
}

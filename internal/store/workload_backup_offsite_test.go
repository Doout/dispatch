package store

import (
	"context"
	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupObjectStoreSQLiteOwnershipAndCredentialReference(t *testing.T) {
	testBackupObjectStore(t, filepath.Join(t.TempDir(), "offsite.db"))
}
func TestBackupObjectStorePostgresOwnershipAndCredentialReference(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL to a disposable PostgreSQL database")
	}
	testBackupObjectStore(t, dsn)
}
func testBackupObjectStore(t *testing.T, dsn string) {
	ctx := context.Background()
	data := mutationStore(t, dsn)
	prefix := ulid.Make().String()
	now := time.Now().UTC()
	project := core.Project{ID: prefix + "project", Name: "Offsite fixture", CreatedAt: now}
	secret := core.Secret{ID: prefix + "credential", Name: "Scoped signing credential", Type: core.SecretTypeJSON, Source: core.SecretSourceLocal, EnvironmentVariable: "BACKUP_S3", EncryptedValue: "encrypted-fixture", CreatedAt: now, UpdatedAt: now}
	for _, err := range []error{data.CreateProject(ctx, project), data.CreateSecret(ctx, secret)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	item := core.BackupObjectStore{ID: prefix + "store", ProjectID: project.ID, Name: "Recovery", CredentialSecretID: secret.ID, Config: backupstore.Config{Endpoint: "https://store.example.com", Bucket: "backups", Region: "us-east-1", Prefix: "workloads", MaxBytes: 1 << 30}, CreatedAt: now}
	if err := data.CreateBackupObjectStore(ctx, item); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetBackupObjectStore(ctx, item.ID)
	if err != nil || stored.ProjectID != project.ID || stored.CredentialSecretID != secret.ID || stored.Config != item.Config {
		t.Fatal(stored, err)
	}
	items, err := data.ListBackupObjectStores(ctx)
	if err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatal(items, err)
	}
	if err = data.DeleteSecret(ctx, secret.ID); err == nil {
		t.Fatal("registered signing credential was deleted")
	}
	invalid := item
	invalid.ID = prefix + "invalid"
	invalid.CredentialSecretID = "missing"
	if err = data.CreateBackupObjectStore(ctx, invalid); err == nil {
		t.Fatal("missing credential reference accepted")
	}
	invalid = item
	invalid.ID = prefix + "large"
	invalid.Config.MaxBytes = backupstore.MaxObjectBytes + 1
	if err = data.CreateBackupObjectStore(ctx, invalid); err == nil {
		t.Fatal("unbounded object registration accepted")
	}
	if _, err = data.GetBackupObjectStore(ctx, "missing"); err != ErrNotFound {
		t.Fatal("missing destination did not return not found", err)
	}
}

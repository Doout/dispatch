package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

func TestBackupVerifiesIsolatedRestoreAndDetectsTampering(t *testing.T) {
	testBackup(t, filepath.Join(t.TempDir(), "live.db"))
}
func TestBackupPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_BACKUP_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_BACKUP_POSTGRES_URL to a disposable PostgreSQL database with CREATE DATABASE permission")
	}
	testBackup(t, dsn)
}
func testBackup(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	must(os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600))
	vault, err := secretcrypto.OpenFile(key)
	must(err)
	cipher, err := vault.Encrypt("secret:backup-test", []byte("backup-check-value"))
	must(err)
	must(s.CreateSecret(ctx, core.Secret{ID: "backup-test", Name: "backup-test", Type: "generic", EncryptedValue: cipher, CreatedAt: time.Now(), UpdatedAt: time.Now()}))
	manager := Manager{Store: s, Directory: filepath.Join(dir, "backups"), MasterKeyFile: key, DatabaseURL: dsn}
	b, err := manager.Create(ctx)
	must(err)
	if b.State != "ready" || b.Bytes < 1 {
		t.Fatalf("backup incomplete: %+v", b)
	}
	must(s.CreateProject(ctx, core.Project{ID: "after-backup", Name: "after-backup", CreatedAt: time.Now()}))
	verified, err := manager.Verify(ctx, b.ID)
	must(err)
	if verified.State != "verified" || verified.VerifiedAt == nil {
		t.Fatal("restore not verified")
	}
	if _, err = s.GetProject(ctx, "after-backup"); err != nil {
		t.Fatal("restore verification altered live database")
	}
	keyCopy := filepath.Join(manager.Directory, b.ID, "master.key")
	must(os.WriteFile(keyCopy, []byte(strings.Repeat("x", 32)), 0600))
	failed, err := manager.Verify(ctx, b.ID)
	if err == nil || failed.State != "verification_failed" || failed.VerifiedAt != nil {
		t.Fatal("tampered backup shown verified")
	}
	if _, err = manager.Verify(ctx, "../../live.db"); err == nil {
		t.Fatal("backup path traversal accepted")
	}
}

func TestBackupManifestFailureClearsPreviousVerification(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	key := filepath.Join(root, "key")
	if err = os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Store: s, Directory: filepath.Join(root, "backups"), MasterKeyFile: key}
	b, err := manager.Create(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Verify(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(manager.Directory, b.ID, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	failed, err := manager.Verify(ctx, b.ID)
	if err == nil || failed.State != "verification_failed" || failed.VerifiedAt != nil {
		t.Fatal("missing manifest retained successful verification")
	}
	records, err := s.ListBackupRecords(ctx)
	if err != nil || len(records) != 1 || records[0].State != "verification_failed" || records[0].VerifiedAt != nil {
		t.Fatal("failure was not persisted")
	}
}

package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestControllerBackupRetentionKeepsVerifiedCopiesAndUnownedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s, err := store.Open(ctx, filepath.Join(root, "live.db"))
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
	key := filepath.Join(root, "key")
	must(os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600))
	m := Manager{Store: s, Directory: filepath.Join(root, "backups"), MasterKeyFile: key}
	records := []core.BackupRecord{}
	for i := 0; i < 4; i++ {
		b, err := m.Create(ctx)
		must(err)
		b, err = m.Verify(ctx, b.ID)
		must(err)
		records = append(records, b)
	}
	unverified, err := m.Create(ctx)
	must(err)
	loose := filepath.Join(m.Directory, "legacy-manual.db")
	must(os.WriteFile(loose, []byte("unowned recovery copy"), 0600))
	// Pruned metadata must not hide older verified archives behind the UI's limit.
	for i := 0; i < 110; i++ {
		must(s.SaveBackupRecord(ctx, core.BackupRecord{ID: ulid.Make().String(), State: "pruned", CreatedAt: time.Now().Add(time.Hour)}))
	}
	removed, err := m.PruneVerified(ctx, DefaultVerifiedCopies)
	must(err)
	if removed != 2 {
		t.Fatalf("expected two older backups pruned, got %d", removed)
	}
	for i, b := range records {
		_, err := os.Stat(filepath.Join(m.Directory, b.ID))
		if i < 2 && !errors.Is(err, os.ErrNotExist) {
			t.Fatal("old backup retained")
		}
		if i >= 2 && err != nil {
			t.Fatal("verified recovery copy lost", err)
		}
	}
	for _, path := range []string{loose, filepath.Join(m.Directory, unverified.ID)} {
		if _, err = os.Stat(path); err != nil {
			t.Fatal("unowned or unverified copy removed", err)
		}
	}
	if _, err = m.Verify(ctx, records[0].ID); err == nil {
		t.Fatal("pruned archive offered as restorable")
	}
	again, err := m.PruneVerified(ctx, 2)
	must(err)
	if again != 0 {
		t.Fatal("backup pruning not idempotent")
	}
	// A failed integrity check on either kept copy must stop further pruning.
	b, err := m.Create(ctx)
	must(err)
	b, err = m.Verify(ctx, b.ID)
	must(err)
	must(os.WriteFile(filepath.Join(m.Directory, b.ID, "master.key"), []byte("changed"), 0600))
	if _, err = m.PruneVerified(ctx, 2); err == nil {
		t.Fatal("corrupted kept recovery copy allowed pruning")
	}
	if _, err = os.Stat(filepath.Join(m.Directory, records[2].ID)); err != nil {
		t.Fatal("older valid recovery copy lost after failed check", err)
	}
}

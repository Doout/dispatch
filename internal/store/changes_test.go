package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestChangesOnlyAfterSuccessfulWrite(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "watch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.db.ExecContext(ctx, "CREATE TABLE watch_test (id INTEGER)"); err != nil {
		t.Fatal(err)
	}
	changed := s.Changes()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO watch_test VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Fatal("notified before commit")
	default:
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
		t.Fatal("notified rollback")
	default:
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO watch_test VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("missing committed change")
	}
	changed = s.Changes()
	if _, err = s.db.ExecContext(ctx, "INSERT INTO missing_table VALUES (1)"); err == nil {
		t.Fatal("expected failure")
	}
	select {
	case <-changed:
		t.Fatal("notified failed write")
	default:
	}
	if _, err = s.db.ExecContext(ctx, "INSERT INTO watch_test VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("missing direct write")
	}
}

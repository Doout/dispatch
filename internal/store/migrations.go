package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// migrations keeps the install as one binary without hiding schema design in Go.
//
//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrations embed.FS

func (s *SQLStore) Migrate(ctx context.Context) error {
	dialect := "sqlite"
	if s.postgres {
		dialect = "postgres"
	}

	files, err := fs.Glob(migrations, "migrations/"+dialect+"/*.sql")
	if err != nil {
		return fmt.Errorf("discover %s migrations: %w", dialect, err)
	}
	if len(files) == 0 {
		return fmt.Errorf("no migrations found for %s", dialect)
	}

	for _, name := range files {
		contents, err := migrations.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		version := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		if strings.HasPrefix(version, "000_") {
			if _, err := s.db.ExecContext(ctx, string(contents)); err != nil {
				return fmt.Errorf("prepare migration ledger: %w", err)
			}
			continue
		}

		var applied int
		if err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM schema_migrations WHERE version=?`), version).Scan(&applied); err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if applied > 0 {
			continue
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, string(contents)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, CURRENT_TIMESTAMP)`), version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
	}
	return nil
}

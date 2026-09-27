package store

import (
	"context"
	"embed"
	"fmt"
	"regexp"
	"strings"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrations embed.FS

func (s *SQLStore) Migrate(ctx context.Context) error {
	dialect := "sqlite"
	if s.postgres {
		dialect = "postgres"
	}

	contents, err := migrations.ReadFile("migrations/" + dialect + "/migrations.sql")
	if err != nil {
		return fmt.Errorf("read %s migrations: %w", dialect, err)
	}
	steps, err := parseMigrations(string(contents))
	if err != nil {
		return fmt.Errorf("parse %s migrations: %w", dialect, err)
	}
	return s.applyMigrations(ctx, steps)
}

type migration struct {
	version string
	sql     string
}

var migrationVersion = regexp.MustCompile(`^[0-9]{3}_[a-z0-9_]+$`)

// Version markers split migrations without splitting SQL statements or trigger bodies.
func parseMigrations(contents string) ([]migration, error) {
	var steps []migration
	for _, line := range strings.Split(contents, "\n") {
		if version, ok := strings.CutPrefix(line, "-- dispatch:migration "); ok {
			version = strings.TrimSpace(version)
			if !migrationVersion.MatchString(version) || len(steps) > 0 && version[:3] <= steps[len(steps)-1].version[:3] {
				return nil, fmt.Errorf("invalid or unordered migration version %q", version)
			}
			steps = append(steps, migration{version: version})
		} else if len(steps) > 0 {
			steps[len(steps)-1].sql += line + "\n"
		} else if strings.TrimSpace(line) != "" {
			return nil, fmt.Errorf("SQL appears before the first migration marker")
		}
	}
	if len(steps) == 0 || steps[0].version != "000_schema_migrations" {
		return nil, fmt.Errorf("first migration must create the schema ledger")
	}
	for _, step := range steps {
		if strings.TrimSpace(step.sql) == "" {
			return nil, fmt.Errorf("migration %s is empty", step.version)
		}
	}
	return steps, nil
}

func (s *SQLStore) applyMigrations(ctx context.Context, steps []migration) error {
	for _, step := range steps {
		version, contents := step.version, step.sql
		if strings.HasPrefix(version, "000_") {
			if _, err := s.db.ExecContext(ctx, contents); err != nil {
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
		foreignKeysOff := !s.postgres && strings.Contains(contents, "-- dispatch:foreign-keys-off")
		if foreignKeysOff {
			if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
				return fmt.Errorf("disable foreign keys for migration %s: %w", version, err)
			}
		}

		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			if foreignKeysOff {
				_, _ = s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
			}
			return fmt.Errorf("begin migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, contents); err != nil {
			_ = tx.Rollback()
			if foreignKeysOff {
				_, _ = s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
			}
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO schema_migrations(version, applied_at) VALUES(?, CURRENT_TIMESTAMP)`), version); err != nil {
			_ = tx.Rollback()
			if foreignKeysOff {
				_, _ = s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
			}
			return fmt.Errorf("record migration %s: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			if foreignKeysOff {
				_, _ = s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`)
			}
			return fmt.Errorf("commit migration %s: %w", version, err)
		}
		if foreignKeysOff {
			if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
				return fmt.Errorf("restore foreign keys after migration %s: %w", version, err)
			}
		}
	}
	return nil
}

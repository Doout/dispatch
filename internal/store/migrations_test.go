package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func migrationSteps(t *testing.T, dialect string) []migration {
	t.Helper()
	contents, err := migrations.ReadFile("migrations/" + dialect + "/migrations.sql")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := parseMigrations(string(contents))
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestMigrationBundlesUseTheSameVersions(t *testing.T) {
	versions := func(steps []migration) []string {
		var result []string
		for _, step := range steps {
			result = append(result, step.version)
		}
		return result
	}
	if !reflect.DeepEqual(versions(migrationSteps(t, "sqlite")), versions(migrationSteps(t, "postgres"))) {
		t.Fatal("SQLite and PostgreSQL migration versions differ")
	}
}

func TestMigrationBundleRejectsInvalidBoundaries(t *testing.T) {
	ledger := "-- dispatch:migration 000_schema_migrations\nCREATE TABLE schema_migrations(version TEXT);\n"
	for name, contents := range map[string]string{
		"missing ledger":   "-- dispatch:migration 001_initial\nSELECT 1;",
		"unversioned SQL":  "SELECT 1;\n" + ledger,
		"empty step":       ledger + "-- dispatch:migration 001_initial\n",
		"duplicate number": ledger + "-- dispatch:migration 001_initial\nSELECT 1;\n-- dispatch:migration 001_other\nSELECT 2;",
		"out of order":     ledger + "-- dispatch:migration 002_second\nSELECT 1;\n-- dispatch:migration 001_first\nSELECT 2;",
		"invalid version":  ledger + "-- dispatch:migration latest\nSELECT 1;",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMigrations(contents); err == nil {
				t.Fatal("invalid migration bundle accepted")
			}
		})
	}
}

func TestMigrationBundleAcceptsWindowsLineEndings(t *testing.T) {
	contents, err := migrations.ReadFile("migrations/sqlite/migrations.sql")
	if err != nil {
		t.Fatal(err)
	}
	steps, err := parseMigrations(strings.ReplaceAll(string(contents), "\n", "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range migrationSteps(t, "sqlite") {
		if steps[i].version != want.version || strings.ReplaceAll(steps[i].sql, "\r\n", "\n") != want.sql {
			t.Fatalf("line endings changed migration %s", want.version)
		}
	}
}

func TestSQLiteBundledUpgradesPreserveData(t *testing.T) {
	steps := migrationSteps(t, "sqlite")
	for _, through := range []string{"028", "032", "033", "037", "047", "056"} {
		t.Run(through, func(t *testing.T) {
			ctx := context.Background()
			data, err := Open(ctx, filepath.Join(t.TempDir(), "upgrade.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = data.Close() })
			last := 0
			for last < len(steps) && steps[last].version[:3] <= through {
				last++
			}
			if err := data.applyMigrations(ctx, steps[:last]); err != nil {
				t.Fatal(err)
			}
			_, err = data.db.ExecContext(ctx, `INSERT INTO projects(id,name,description,created_at) VALUES('saved','Saved project','keep me','2026-01-01')`)
			if err != nil {
				t.Fatal(err)
			}
			if through >= "031" {
				_, err = data.db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,password_hash,created_at,updated_at) VALUES('saved-user','saved-user','Saved user','saved-hash','2026-01-01','2026-01-01');
					INSERT INTO admin_sessions(token_hash,user_id,expires_at,created_at) VALUES('saved-session','saved-user','2099-01-01','2026-01-01')`)
				if err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := data.Migrate(ctx); err != nil {
					t.Fatal(err)
				}
			}
			project, err := data.GetProject(ctx, "saved")
			if err != nil || project.Description != "keep me" {
				t.Fatalf("saved project changed: %+v, %v", project, err)
			}
			if through >= "031" {
				var userID, hash string
				err := data.db.QueryRowContext(ctx, `SELECT s.user_id,u.password_hash FROM admin_sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash='saved-session'`).Scan(&userID, &hash)
				if err != nil || userID != "saved-user" || hash != "saved-hash" {
					t.Fatalf("saved session or credentials changed: %q, %q, %v", userID, hash, err)
				}
			}
			var count, foreignKeys int
			if err := data.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != len(steps)-1 {
				t.Fatalf("migration ledger: count=%d, err=%v", count, err)
			}
			if err := data.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
				t.Fatalf("foreign key enforcement: %d, %v", foreignKeys, err)
			}
			rows, err := data.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			if rows.Next() || rows.Err() != nil {
				t.Fatal("foreign key relationships changed during upgrade")
			}
		})
	}
}

func TestSQLiteFailedMigrationRollsBackAndRestoresForeignKeys(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	step := migration{version: "999_failure", sql: "-- dispatch:foreign-keys-off\nCREATE TABLE partial_change(id TEXT); INSERT INTO missing_table VALUES(1);"}
	if err := data.applyMigrations(ctx, []migration{step}); err == nil || !strings.Contains(err.Error(), step.version) {
		t.Fatalf("expected a versioned migration error, got %v", err)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM sqlite_master WHERE name='partial_change'`:     0,
		`SELECT COUNT(*) FROM schema_migrations WHERE version='999_failure'`: 0,
		`PRAGMA foreign_keys`: 1,
	} {
		var got int
		if err := data.db.QueryRowContext(ctx, query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: got %d, want %d: %v", query, got, want, err)
		}
	}
}

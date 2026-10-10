package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type Catalog struct {
	db       *sql.DB
	postgres bool
	now      func() time.Time
}

func Open(ctx context.Context, databaseURL string) (*Catalog, error) {
	if strings.TrimSpace(databaseURL) == "" {
		return nil, ErrInvalid
	}
	postgres := strings.HasPrefix(databaseURL, "postgres://") || strings.HasPrefix(databaseURL, "postgresql://")
	driver, dsn := "pgx", databaseURL
	if !postgres {
		if strings.ContainsAny(databaseURL, "?#") {
			return nil, ErrInvalid
		}
		info, err := os.Lstat(databaseURL)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("hosted catalog must be a private regular file")
		}
		if err := os.MkdirAll(filepath.Dir(databaseURL), 0700); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(databaseURL, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
		driver, dsn = "sqlite", "file:"+databaseURL+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	if !postgres {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &Catalog{db: db, postgres: postgres, now: time.Now}, nil
}

func (c *Catalog) Close() error { return c.db.Close() }
func (c *Catalog) q(query string) string {
	if !c.postgres {
		return query
	}
	var out strings.Builder
	n := 0
	for _, char := range query {
		if char == '?' {
			n++
			out.WriteString("$" + strconv.Itoa(n))
		} else {
			out.WriteRune(char)
		}
	}
	return out.String()
}
func (c *Catalog) locking(query string) string {
	if c.postgres {
		return query + " FOR UPDATE"
	}
	return query
}

// Migrate initializes only the catalog selected by Open. Operational database
// migrations are separate and are applied by Factory to a tenant's own database.
func (c *Catalog) Migrate(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.postgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7136449551)`); err != nil {
			return err
		}
	}
	for _, statement := range catalogSchema {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize tenant catalog: %w", err)
		}
	}
	return tx.Commit()
}

var catalogSchema = []string{
	`CREATE TABLE IF NOT EXISTS tenancy_schema (version INTEGER PRIMARY KEY)`,
	`CREATE TABLE IF NOT EXISTS tenancy_users (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE, name TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('active','disabled','pending')), platform_admin INTEGER NOT NULL DEFAULT 0, email_verified INTEGER NOT NULL DEFAULT 0, password_hash TEXT NOT NULL DEFAULT '', version BIGINT NOT NULL DEFAULT 1, created_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenants (id TEXT PRIMARY KEY, slug TEXT NOT NULL UNIQUE, name TEXT NOT NULL, state TEXT NOT NULL CHECK(state IN ('pending','provisioning','active','failed')), created_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenant_memberships (tenant_id TEXT NOT NULL REFERENCES tenants(id), user_id TEXT NOT NULL REFERENCES tenancy_users(id), role TEXT NOT NULL CHECK(role IN ('owner','admin','member')), state TEXT NOT NULL CHECK(state IN ('active','disabled')), version BIGINT NOT NULL DEFAULT 1, created_at BIGINT NOT NULL, PRIMARY KEY(tenant_id,user_id))`,
	`CREATE INDEX IF NOT EXISTS tenant_memberships_user ON tenant_memberships(user_id,state)`,
	`CREATE TABLE IF NOT EXISTS tenant_invitations (tenant_id TEXT PRIMARY KEY REFERENCES tenants(id), email TEXT NOT NULL, created_at BIGINT NOT NULL, accepted_by TEXT REFERENCES tenancy_users(id))`,
	`CREATE TABLE IF NOT EXISTS tenancy_sessions (token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES tenancy_users(id), audience TEXT NOT NULL, user_version BIGINT NOT NULL, membership_version BIGINT NOT NULL DEFAULT 0, expires_at BIGINT NOT NULL, created_at BIGINT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS tenancy_sessions_user ON tenancy_sessions(user_id)`,
	`CREATE INDEX IF NOT EXISTS tenancy_sessions_expiry ON tenancy_sessions(expires_at)`,
	`CREATE TABLE IF NOT EXISTS tenant_handoffs (code_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES tenancy_users(id), tenant_id TEXT NOT NULL REFERENCES tenants(id), origin TEXT NOT NULL, code_challenge TEXT NOT NULL, user_version BIGINT NOT NULL, membership_version BIGINT NOT NULL, expires_at BIGINT NOT NULL, created_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenancy_email_verifications (token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES tenancy_users(id), email TEXT NOT NULL, expires_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenant_certificate_tokens (tenant_id TEXT PRIMARY KEY REFERENCES tenants(id), token_hash TEXT NOT NULL UNIQUE, user_id TEXT NOT NULL REFERENCES tenancy_users(id), membership_version BIGINT NOT NULL, expires_at BIGINT NOT NULL, created_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenant_usage (tenant_id TEXT NOT NULL REFERENCES tenants(id), period_start BIGINT NOT NULL, period_end BIGINT NOT NULL, projects BIGINT NOT NULL, applications BIGINT NOT NULL, members BIGINT NOT NULL, builds BIGINT NOT NULL, deployments BIGINT NOT NULL, build_seconds BIGINT NOT NULL, runtime_seconds BIGINT NOT NULL, storage_bytes BIGINT NOT NULL, transfer_bytes BIGINT NOT NULL, updated_at BIGINT NOT NULL, measured TEXT NOT NULL DEFAULT '[]', PRIMARY KEY(tenant_id,period_start,period_end))`,
	`CREATE TABLE IF NOT EXISTS tenant_domains (id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL REFERENCES tenants(id), hostname TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, state TEXT NOT NULL, claim_hash TEXT NOT NULL, created_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenant_provisioning (tenant_id TEXT PRIMARY KEY REFERENCES tenants(id), state TEXT NOT NULL, phase TEXT NOT NULL, updated_at BIGINT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS tenant_zone_generation (id INTEGER PRIMARY KEY CHECK(id=1), generation BIGINT NOT NULL)`,
	`INSERT INTO tenant_zone_generation(id,generation) VALUES(1,0) ON CONFLICT(id) DO NOTHING`,
	`CREATE TABLE IF NOT EXISTS tenant_zone_records (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, tenant_id TEXT NOT NULL, name TEXT NOT NULL, type TEXT NOT NULL, values_json TEXT NOT NULL, ttl BIGINT NOT NULL, generation BIGINT NOT NULL)`,
	`INSERT INTO tenancy_schema(version) VALUES(1) ON CONFLICT(version) DO NOTHING`,
}

type scanner interface{ Scan(...any) error }

func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
func conflict(err error) error {
	if err == nil {
		return nil
	}
	type stateError interface{ SQLState() string }
	var state stateError
	if errors.As(err, &state) && state.SQLState() == "23505" || strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}
func millis(t time.Time) int64  { return t.UTC().UnixMilli() }
func instant(v int64) time.Time { return time.UnixMilli(v).UTC() }
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

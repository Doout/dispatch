package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestFactoryPostgresRestrictedProvisioner(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = os.Getenv("DISPATCH_STORE_POSTGRES_URL")
	}
	if dsn == "" {
		t.Skip("disposable PostgreSQL URL is not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var version int
	if err := admin.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 160000 {
		t.Skip("hosted tenant provisioning requires PostgreSQL 16 or later")
	}
	provisioner := "dispatch_provisioner_" + strings.ToLower(ulid.Make().String())
	password, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = admin.ExecContext(ctx, `CREATE ROLE "`+provisioner+`" LOGIN NOSUPERUSER CREATEDB CREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '`+password+`'`); err != nil {
		t.Fatal(err)
	}
	ids := []string{ulid.Make().String(), ulid.Make().String()}
	root := filepath.Join(t.TempDir(), "tenants")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(provisioner, password)
	query := u.Query()
	query.Del("options")
	query.Del("role")
	query.Del("user")
	query.Del("password")
	u.RawQuery = query.Encode()
	f, err := NewFactory(FactoryConfig{RootDir: root, PostgresAdminURL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.Close()
		for _, id := range ids {
			name := "dispatch_t_" + strings.ToLower(id)
			if _, err := admin.ExecContext(ctx, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
				t.Error(err)
			}
			if _, err := admin.ExecContext(ctx, `DROP ROLE IF EXISTS "`+name+`"`); err != nil {
				t.Error(err)
			}
		}
		if _, err := admin.ExecContext(ctx, `DROP ROLE "`+provisioner+`"`); err != nil {
			t.Error(err)
		}
	})
	// Reproduce a failed earlier provisioning attempt that persisted credentials
	// and created the role, but never reached CREATE DATABASE.
	pending := "dispatch_t_" + strings.ToLower(ids[1])
	credential := databaseCredential{Database: pending, Role: pending}
	credential.Password, err = randomToken()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, ids[1])
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "database.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	provisionerDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer provisionerDB.Close()
	if _, err := provisionerDB.ExecContext(ctx, `CREATE ROLE "`+pending+`" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '`+credential.Password+`'`); err != nil {
		t.Fatal(err)
	}
	var canSet bool
	if err := provisionerDB.QueryRowContext(ctx, `SELECT pg_has_role(current_user,$1,'SET')`, pending).Scan(&canSet); err != nil || canSet {
		t.Fatal("fixture did not reproduce restricted creator membership", canSet, err)
	}
	a, err := f.Open(ctx, ids[0])
	if err != nil {
		t.Fatal("provision with CREATEDB/CREATEROLE and no superuser", err)
	}
	b, err := f.Open(ctx, ids[1])
	if err != nil {
		t.Fatal("retry role-only provisioning", err)
	}
	if err := a.Store.CreateProject(ctx, core.Project{ID: "private", Name: "Private tenant project", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Store.GetProject(ctx, "private"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("tenant data crossed databases", err)
	}
	aDSN, bDSN := a.DatabaseURL, b.DatabaseURL
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	f, err = NewFactory(FactoryConfig{RootDir: root, PostgresAdminURL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	a, err = f.Open(ctx, ids[0])
	if err != nil {
		t.Fatal("retry existing tenant role and database", err)
	}
	if a.DatabaseURL != aDSN {
		t.Fatal("restart rotated persisted tenant identity")
	}
	if _, err := a.Store.GetProject(ctx, "private"); err != nil {
		t.Fatal("restart lost tenant data", err)
	}
	for _, id := range ids {
		name := "dispatch_t_" + strings.ToLower(id)
		var safe bool
		if err := admin.QueryRowContext(ctx, `SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=pg_roles.oid) FROM pg_roles WHERE rolname=$1`, name).Scan(&safe); err != nil || !safe {
			t.Fatal("tenant received extra privileges", safe, err)
		}
		var canSet, inherits bool
		if err := provisionerDB.QueryRowContext(ctx, `SELECT pg_has_role(current_user,$1,'SET'),pg_has_role(current_user,$1,'USAGE')`, name).Scan(&canSet, &inherits); err != nil || !canSet || inherits {
			t.Fatal("provisioner membership must permit explicit SET ROLE without inheritance", canSet, inherits, err)
		}
		var publicPrivileges int
		if err := admin.QueryRowContext(ctx, `SELECT count(*) FROM pg_database d CROSS JOIN LATERAL aclexplode(COALESCE(d.datacl,acldefault('d',d.datdba))) acl WHERE d.datname=$1 AND acl.grantee=0`, name).Scan(&publicPrivileges); err != nil || publicPrivileges != 0 {
			t.Fatal("PUBLIC database access survived provisioning", publicPrivileges, err)
		}
	}
	tenant, err := sql.Open("pgx", aDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer tenant.Close()
	for _, statement := range []string{`SET ROLE "` + provisioner + `"`, `SET ROLE "` + pending + `"`, `CREATE ROLE must_not_create_role`} {
		if _, err := tenant.ExecContext(ctx, statement); err == nil {
			t.Fatal("tenant gained provisioner or sibling privileges", statement)
		}
	}
	otherURL, err := url.Parse(bDSN)
	if err != nil {
		t.Fatal(err)
	}
	foreignURL, err := url.Parse(aDSN)
	if err != nil {
		t.Fatal(err)
	}
	foreignURL.Path = otherURL.Path
	foreign, err := sql.Open("pgx", foreignURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := foreign.PingContext(ctx); err == nil {
		t.Fatal("tenant connected to sibling database")
	}
	var provisionerSafe bool
	if err := admin.QueryRowContext(ctx, `SELECT NOT rolsuper AND rolcreatedb AND rolcreaterole AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls FROM pg_roles WHERE rolname=$1`, provisioner).Scan(&provisionerSafe); err != nil || !provisionerSafe {
		t.Fatal("provisioner gained extra role attributes", err)
	}
}

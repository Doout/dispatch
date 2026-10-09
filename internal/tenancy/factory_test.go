package tenancy

import (
	"context"
	"database/sql"
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

func TestFactoryPhysicallySeparatesOperationalStores(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "tenants")
	f, err := NewFactory(FactoryConfig{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	aID, bID := ulid.Make().String(), ulid.Make().String()
	a, err := f.Open(ctx, aID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Open(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if a.DatabaseURL == b.DatabaseURL || a.Paths.VaultPath == b.Paths.VaultPath || a.Paths.CacheDir == b.Paths.CacheDir || a.Paths.ArtifactDir == b.Paths.ArtifactDir {
		t.Fatal("tenant directories share resources")
	}
	project := core.Project{ID: "same-id", Name: "Private A", CreatedAt: time.Now()}
	if err = a.Store.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Store.GetProject(ctx, project.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("unfiltered lookup crossed database", err)
	}
	project.Name = "Private B"
	if err = b.Store.CreateProject(ctx, project); err != nil {
		t.Fatal("same identifier collided across databases", err)
	}
	if err = a.Store.DeleteProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := b.Store.GetProject(ctx, project.ID); err != nil || got.Name != "Private B" {
		t.Fatal("cross-tenant mutation", got, err)
	}
	if got, err := f.Open(ctx, aID); err != nil || got != a {
		t.Fatal("factory reopened runtime instead of reusing it", err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	f, err = NewFactory(FactoryConfig{RootDir: root})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err = f.Open(ctx, bID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.Store.GetProject(ctx, project.ID); err != nil {
		t.Fatal("restart lost tenant state", err)
	}
	for _, bad := range []string{"../current", "", "../../dispatch.db"} {
		if _, err = f.Open(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe tenant id accepted", err)
		}
	}
	symlinkID := ulid.Make().String()
	if err = os.Symlink(t.TempDir(), filepath.Join(root, symlinkID)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Open(ctx, symlinkID); err == nil {
		t.Fatal("tenant directory symlink accepted")
	}
}

func TestFactoryPostgresDatabaseRoleIsolation(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = os.Getenv("DISPATCH_STORE_POSTGRES_URL")
	}
	if dsn == "" {
		t.Skip("disposable PostgreSQL URL is not configured")
	}
	ctx := context.Background()
	f, err := NewFactory(FactoryConfig{RootDir: filepath.Join(t.TempDir(), "tenants"), PostgresAdminURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{ulid.Make().String(), ulid.Make().String()}
	t.Cleanup(func() {
		f.Close()
		defer admin.Close()
		for _, id := range ids {
			name := "dispatch_t_" + strings.ToLower(id)
			if _, err := admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
				t.Error(err)
			}
			if _, err := admin.ExecContext(context.Background(), `DROP ROLE IF EXISTS "`+name+`"`); err != nil {
				t.Error(err)
			}
		}
	})
	a, err := f.Open(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.Open(ctx, ids[1])
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Store.CreateProject(ctx, core.Project{ID: "private", Name: "Private A", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Store.GetProject(ctx, "private"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("foreign tenant data visible", err)
	}
	connection, err := sql.Open("pgx", a.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	var role, database string
	if err = connection.QueryRowContext(ctx, `SELECT current_user,current_database()`).Scan(&role, &database); err != nil {
		t.Fatal(err)
	}
	expected := "dispatch_t_" + strings.ToLower(ids[0])
	if role != expected || database != expected {
		t.Fatal("runtime inherited provisioner authority")
	}
	if _, err = connection.ExecContext(ctx, `CREATE ROLE unexpected_tenant_elevation`); err == nil {
		t.Fatal("tenant role can create roles")
	}
	u, err := url.Parse(a.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/dispatch_t_" + strings.ToLower(ids[1])
	foreign, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err = foreign.PingContext(ctx); err == nil {
		t.Fatal("tenant role connected to another tenant database")
	}
}

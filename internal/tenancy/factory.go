package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type FactoryConfig struct {
	RootDir string
	// PostgresAdminURL is used only while provisioning a dedicated tenant
	// database and login role. Runtime services receive only the tenant DSN.
	PostgresAdminURL string
}
type RuntimePaths struct {
	RootDir       string
	DataDir       string
	CacheDir      string
	ArtifactDir   string
	VaultPath     string
	DatabasePath  string
	AnalyticsPath string
}
type Runtime struct {
	Store       *store.SQLStore `json:"-"`
	Paths       RuntimePaths    `json:"-"`
	DatabaseURL string          `json:"-"`
}
type Factory struct {
	config   FactoryConfig
	mu       sync.Mutex
	runtimes map[string]*Runtime
	closed   bool
}

func NewFactory(config FactoryConfig) (*Factory, error) {
	if config.RootDir == "" {
		return nil, ErrInvalid
	}
	absolute, err := filepath.Abs(config.RootDir)
	if err != nil {
		return nil, err
	}
	config.RootDir = absolute
	if config.PostgresAdminURL != "" {
		u, err := url.Parse(config.PostgresAdminURL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || u.User == nil {
			return nil, ErrInvalid
		}
	}
	if err = privateDirectory(absolute); err != nil {
		return nil, err
	}
	return &Factory{config: config, runtimes: map[string]*Runtime{}}, nil
}

func privateDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("tenant directory must be a real directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("tenant directory must have private permissions")
	}
	return nil
}

// Open never opens a supplied existing operational database. Names are derived
// from a validated tenant ID beneath the configured private root, or provisioned
// as a dedicated PostgreSQL database. Call only after resolving a catalog tenant.
func (f *Factory) Open(ctx context.Context, tenant string) (*Runtime, error) {
	if _, err := ulid.ParseStrict(tenant); err != nil || tenant != strings.ToUpper(tenant) {
		return nil, ErrInvalid
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil, errors.New("tenant factory closed")
	}
	if r := f.runtimes[tenant]; r != nil {
		return r, nil
	}
	root := filepath.Join(f.config.RootDir, tenant)
	paths := RuntimePaths{RootDir: root, DataDir: filepath.Join(root, "data"), CacheDir: filepath.Join(root, "cache"), ArtifactDir: filepath.Join(root, "artifacts"), VaultPath: filepath.Join(root, "vault", "master.key"), DatabasePath: filepath.Join(root, "data", "dispatch.db"), AnalyticsPath: filepath.Join(root, "data", "analytics.duckdb")}
	for _, dir := range []string{root, paths.DataDir, paths.CacheDir, paths.ArtifactDir, filepath.Dir(paths.VaultPath)} {
		if err := privateDirectory(dir); err != nil {
			return nil, err
		}
	}
	unlock, err := lockFactory(ctx, filepath.Join(root, "provision.lock"))
	if err != nil {
		return nil, err
	}
	defer unlock()
	dsn := paths.DatabasePath
	if f.config.PostgresAdminURL != "" {
		dsn, err = f.postgresDatabase(ctx, tenant, root)
		if err != nil {
			return nil, err
		}
	} else {
		info, err := os.Lstat(dsn)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return nil, errors.New("tenant database must be a private regular file")
		}
		file, err := os.OpenFile(dsn, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, err
		}
		if err = file.Close(); err != nil {
			return nil, err
		}
	}
	db, err := store.Open(ctx, dsn)
	if err != nil {
		return nil, errors.New("open tenant database failed")
	}
	db.ConfigureTenantPool()
	if err = db.Migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate tenant database: %w", err)
	}
	runtime := &Runtime{Store: db, Paths: paths, DatabaseURL: dsn}
	f.runtimes[tenant] = runtime
	return runtime, nil
}

func lockFactory(ctx context.Context, path string) (func(), error) {
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("tenant lock must be a regular file")
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type databaseCredential struct {
	Database string
	Role     string
	Password string
}

func (f *Factory) postgresDatabase(ctx context.Context, tenant, root string) (string, error) {
	name := "dispatch_t_" + strings.ToLower(tenant)
	path := filepath.Join(root, "database.json")
	credential := databaseCredential{Database: name, Role: name}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		credential.Password, err = randomToken()
		if err != nil {
			return "", err
		}
		data, err = json.Marshal(credential)
		if err != nil {
			return "", err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return "", err
		}
		_, err = file.Write(data)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return "", err
		}
		if closeErr != nil {
			return "", closeErr
		}
	} else if err != nil {
		return "", err
	} else {
		info, err := os.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("tenant database credentials must be private")
		}
		if err = json.Unmarshal(data, &credential); err != nil {
			return "", errors.New("invalid tenant database credentials")
		}
	}
	if credential.Database != name || credential.Role != name || len(credential.Password) != 43 || strings.ContainsAny(credential.Password, "'\"\\ \r\n") {
		return "", errors.New("invalid tenant database identity")
	}
	admin, err := sql.Open("pgx", f.config.PostgresAdminURL)
	if err != nil {
		return "", errors.New("open database provisioner failed")
	}
	defer admin.Close()
	conn, err := admin.Conn(ctx)
	if err != nil {
		return "", errors.New("connect database provisioner failed")
	}
	defer conn.Close()
	// This session lock covers CREATE DATABASE, which cannot run in a transaction.
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext($1))`, name); err != nil {
		return "", errors.New("lock tenant database provisioning failed")
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(hashtext($1))`, name)
	var exists bool
	if err = conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname=$1)`, name).Scan(&exists); err != nil {
		return "", errors.New("inspect tenant database role failed")
	}
	if !exists {
		// The identifier is derived from a validated ULID. The password is random
		// base64url. Neither can contain SQL syntax or a quote.
		if _, err = conn.ExecContext(ctx, `CREATE ROLE "`+name+`" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS PASSWORD '`+credential.Password+`'`); err != nil {
			return "", errors.New("create tenant database role failed")
		}
	}
	var safe bool
	err = conn.QueryRowContext(ctx, `SELECT NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls AND rolcanlogin AND NOT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=pg_roles.oid) FROM pg_roles WHERE rolname=$1`, name).Scan(&safe)
	if err != nil || !safe {
		return "", errors.New("tenant database role has unexpected privileges")
	}
	var owner string
	err = conn.QueryRowContext(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=$1`, name).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = conn.ExecContext(ctx, `CREATE DATABASE "`+name+`" OWNER "`+name+`"`); err != nil {
			return "", errors.New("create tenant database failed")
		}
	} else if err != nil {
		return "", errors.New("inspect tenant database failed")
	} else if owner != name {
		return "", errors.New("tenant database has unexpected owner")
	}
	if _, err = conn.ExecContext(ctx, `REVOKE ALL ON DATABASE "`+name+`" FROM PUBLIC`); err != nil {
		return "", errors.New("restrict tenant database failed")
	}
	u, err := url.Parse(f.config.PostgresAdminURL)
	if err != nil {
		return "", ErrInvalid
	}
	u.User = url.UserPassword(name, credential.Password)
	u.Path = "/" + name
	u.RawPath = ""
	// A provisioner DSN may select another role or search path. Never propagate
	// session role overrides to tenant connections.
	query := u.Query()
	for _, key := range []string{"options", "role", "search_path", "dbname", "user", "password"} {
		query.Del(key)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func (f *Factory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	var errs []error
	for id, runtime := range f.runtimes {
		if err := runtime.Store.Close(); err != nil {
			errs = append(errs, err)
		}
		delete(f.runtimes, id)
	}
	return errors.Join(errs...)
}

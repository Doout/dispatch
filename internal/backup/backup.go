// Package backup creates consistent controller backups and verifies isolated restores.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

type Repository interface {
	DatabaseEngine() string
	BackupSQLite(context.Context, string) error
	SaveBackupRecord(context.Context, core.BackupRecord) error
	ListBackupRecords(context.Context) ([]core.BackupRecord, error)
}
type Manager struct {
	Store                                 Repository
	Directory, MasterKeyFile, DatabaseURL string
}
type manifest struct {
	Engine string
	Files  map[string]string
	Counts map[string]int
}

func (m Manager) Create(ctx context.Context) (core.BackupRecord, error) {
	b := core.BackupRecord{ID: ulid.Make().String(), Engine: m.Store.DatabaseEngine(), State: "creating", CreatedAt: time.Now().UTC(), Message: "Creating a consistent controller backup."}
	if m.Directory == "" || m.MasterKeyFile == "" {
		return b, errors.New("controller backup directory and master key must be configured")
	}
	if err := os.MkdirAll(m.Directory, 0700); err != nil {
		return b, err
	}
	dir := filepath.Join(m.Directory, b.ID)
	if err := os.Mkdir(dir, 0700); err != nil {
		return b, err
	}
	if err := m.Store.SaveBackupRecord(ctx, b); err != nil {
		return b, err
	}
	fail := func(err error) (core.BackupRecord, error) {
		b.State = "failed"
		b.Message = "Backup failed. Check available storage, database access, and backup tools."
		_ = m.Store.SaveBackupRecord(context.Background(), b)
		_ = os.RemoveAll(dir)
		return b, err
	}
	if err := copyPrivate(m.MasterKeyFile, filepath.Join(dir, "master.key")); err != nil {
		return fail(err)
	}
	name := "database.sqlite"
	var err error
	if b.Engine == "postgresql" {
		name = "database.dump"
		err = postgresTool(ctx, m.DatabaseURL, "pg_dump", "--format=custom", "--no-owner", "--no-acl", "--file="+filepath.Join(dir, name))
	} else {
		err = m.Store.BackupSQLite(ctx, filepath.Join(dir, name))
	}
	if err != nil {
		return fail(err)
	}
	if err = os.Chmod(filepath.Join(dir, name), 0600); err != nil {
		return fail(err)
	}
	meta := manifest{Engine: b.Engine, Files: map[string]string{}}
	for _, file := range []string{name, "master.key"} {
		sum, n, err := checksum(filepath.Join(dir, file))
		if err != nil {
			return fail(err)
		}
		meta.Files[file] = sum
		b.Bytes += n
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fail(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0600); err != nil {
		return fail(err)
	}
	b.State = "ready"
	b.Message = "Backup created. Run a restore check to verify the database and vault key together."
	err = m.Store.SaveBackupRecord(ctx, b)
	return b, err
}
func (m Manager) Verify(ctx context.Context, id string) (core.BackupRecord, error) {
	if _, err := ulid.ParseStrict(id); err != nil {
		return core.BackupRecord{}, errors.New("invalid backup ID")
	}
	records, err := m.Store.ListBackupRecords(ctx)
	if err != nil {
		return core.BackupRecord{}, err
	}
	var b core.BackupRecord
	for _, r := range records {
		if r.ID == id {
			b = r
		}
	}
	if b.ID == "" {
		return b, store.ErrNotFound
	}
	dir := filepath.Join(m.Directory, id)
	check := func() error {
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			return errors.New("backup manifest is unavailable")
		}
		var meta manifest
		if err = json.Unmarshal(raw, &meta); err != nil {
			return errors.New("backup manifest is invalid")
		}
		name := "database.sqlite"
		if b.Engine == "postgresql" {
			name = "database.dump"
		}
		if meta.Engine != b.Engine || len(meta.Files) != 2 {
			return errors.New("backup manifest is invalid")
		}
		for _, file := range []string{name, "master.key"} {
			sum, _, err := checksum(filepath.Join(dir, file))
			if err != nil || sum != meta.Files[file] {
				return errors.New("backup checksum does not match")
			}
		}
		vault, err := secretcrypto.OpenFile(filepath.Join(dir, "master.key"))
		if err != nil {
			return errors.New("backup vault key is invalid")
		}
		var restored *store.SQLStore
		if b.Engine == "sqlite" {
			temp, err := os.MkdirTemp(m.Directory, ".restore-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(temp)
			if err = copyPrivate(filepath.Join(dir, name), filepath.Join(temp, "database.sqlite")); err != nil {
				return err
			}
			restored, err = store.Open(ctx, filepath.Join(temp, "database.sqlite"))
			if err != nil {
				return err
			}
			defer restored.Close()
			if err = restored.CheckSQLite(ctx); err != nil {
				return err
			}
		} else {
			dsn, cleanup, err := m.restorePostgres(ctx, filepath.Join(dir, name))
			if err != nil {
				return err
			}
			defer cleanup()
			restored, err = store.Open(ctx, dsn)
			if err != nil {
				return err
			}
			defer restored.Close()
		}
		// Run schema upgrades only in the isolated restore, never against the source backup.
		if err = restored.Migrate(ctx); err != nil {
			return err
		}
		if _, err = restored.ListProjects(ctx); err != nil {
			return err
		}
		if _, err = restored.ListApps(ctx); err != nil {
			return err
		}
		secrets, err := restored.ListSecrets(ctx)
		if err != nil {
			return err
		}
		for _, s := range secrets {
			if s.EncryptedValue == "" {
				continue
			}
			plain, err := vault.Decrypt("secret:"+s.ID, s.EncryptedValue)
			clear(plain)
			if err != nil {
				return errors.New("restored vault cannot decrypt a saved secret")
			}
		}
		services, err := restored.ListServices(ctx, "")
		if err != nil {
			return err
		}
		for _, s := range services {
			for name, f := range s.Fields {
				if f.EncryptedValue == "" {
					continue
				}
				plain, err := vault.Decrypt(serviceconn.FieldAAD(s.ID, name), f.EncryptedValue)
				clear(plain)
				if err != nil {
					return errors.New("restored vault cannot decrypt service credentials")
				}
			}
		}
		return nil
	}
	err = check()
	if err != nil {
		b.State = "verification_failed"
		b.VerifiedAt = nil
		b.Message = "Restore verification failed. The backup was retained for inspection."
	} else {
		now := time.Now().UTC()
		b.State = "verified"
		b.VerifiedAt = &now
		b.Message = "An isolated restore passed database, schema, and saved credential checks."
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if saveErr := m.Store.SaveBackupRecord(saveCtx, b); saveErr != nil {
		return b, saveErr
	}
	return b, err
}
func (m Manager) restorePostgres(ctx context.Context, path string) (string, func(), error) {
	parsed, err := url.Parse(m.DatabaseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return "", nil, errors.New("PostgreSQL backup requires a connection URL")
	}
	admin, err := sql.Open("pgx", m.DatabaseURL)
	if err != nil {
		return "", nil, err
	}
	name := "dispatch_restore_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		admin.Close()
		return "", nil, errors.New("restore verification requires permission to create an isolated PostgreSQL database")
	}
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.ExecContext(c, `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
		_ = admin.Close()
	}
	parsed.Path = "/" + name
	query := parsed.Query()
	query.Del("dbname")
	parsed.RawQuery = query.Encode()
	dsn := parsed.String()
	if err = postgresTool(ctx, dsn, "pg_restore", "--exit-on-error", "--no-owner", "--no-acl", "--dbname="+name, path); err != nil {
		cleanup()
		return "", nil, err
	}
	return dsn, cleanup, nil
}
func postgresTool(ctx context.Context, dsn, name string, args ...string) error {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Hostname() == "" {
		return errors.New("invalid PostgreSQL connection")
	}
	database := strings.TrimPrefix(parsed.Path, "/")
	values := map[string]string{"PGHOST": parsed.Hostname(), "PGPORT": parsed.Port(), "PGDATABASE": database}
	if parsed.User != nil {
		password, _ := parsed.User.Password()
		values["PGUSER"] = parsed.User.Username()
		values["PGPASSWORD"] = password
	}
	for key, value := range parsed.Query() {
		if len(value) > 0 {
			switch key {
			case "sslmode", "sslrootcert", "sslcert", "sslkey", "connect_timeout", "options", "channel_binding", "ssl_min_protocol_version", "ssl_max_protocol_version":
				values["PG"+strings.ToUpper(key)] = value[0]
			}
		}
	}
	filtered := []string{}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "--dbname=") {
			filtered = append(filtered, arg)
		}
	}
	if name == "pg_restore" {
		filtered = append([]string{"--dbname=" + database}, filtered...)
	}
	cmd := exec.CommandContext(ctx, name, filtered...)
	// Supply libpq fields separately: PGDATABASE does not expand a connection URL.
	// Connection credentials never appear in command arguments or logs.
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "PG") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	for key, value := range values {
		if value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("%s failed; ensure compatible PostgreSQL client tools and database permissions are available", name)
	}
	return nil
}

func copyPrivate(source, dest string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}
func checksum(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, f)
	return hex.EncodeToString(hash.Sum(nil)), n, err
}

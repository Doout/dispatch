package installation

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

// Check the database and vault together in an isolated copy. Schema upgrades
// here never modify the archive or the destination volume.
func verifyExportDatabase(ctx context.Context, archive string) error {
	dir, err := os.MkdirTemp("", ".dispatch-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer z.Close()
	r := tar.NewReader(z)
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		name := strings.TrimPrefix(h.Name, "./")
		if name != "dispatch.db" && name != "dispatch.db-wal" && name != "dispatch.db-shm" && name != "dispatch-master.key" {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return errors.New("database and vault files must be regular files")
		}
		dst, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(dst, r)
		ce := dst.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	vault, err := secretcrypto.OpenFile(filepath.Join(dir, "dispatch-master.key"))
	if err != nil {
		return errors.New("export vault key is invalid")
	}
	db, err := store.Open(ctx, filepath.Join(dir, "dispatch.db"))
	if err != nil {
		return errors.New("export database cannot be opened")
	}
	defer db.Close()
	if err = db.CheckSQLite(ctx); err != nil {
		return errors.New("export database integrity check failed")
	}
	if err = db.CheckMigrationCompatibility(ctx); err != nil {
		return errors.New("export database needs a newer CLI")
	}
	if err = db.Migrate(ctx); err != nil {
		return errors.New("export database schema is incompatible with this CLI")
	}
	if err = db.VisitControllerCiphertexts(ctx, func(scope, ciphertext string) error {
		plain, err := vault.Decrypt(scope, ciphertext)
		clear(plain)
		return err
	}); err != nil {
		return errors.New("export vault key cannot decrypt the saved credentials")
	}
	return nil
}

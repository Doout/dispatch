package installation

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

func writeTestTar(t *testing.T, file string, headers []*tar.Header, contents [][]byte) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	z := gzip.NewWriter(f)
	w := tar.NewWriter(z)
	for i, h := range headers {
		if err = w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(contents[i]); err != nil {
			t.Fatal(err)
		}
	}
	for _, err = range []error{w.Close(), z.Close(), f.Close()} {
		if err != nil {
			t.Fatal(err)
		}
	}
}
func dataFixture(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	key := filepath.Join(dir, "dispatch-master.key")
	if err := os.WriteFile(key, bytes.Repeat([]byte{'!'}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := vault.Encrypt("secret:portable", []byte("retained credential"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, filepath.Join(dir, "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateSecret(ctx, core.Secret{ID: "portable", Name: "portable", Type: core.SecretTypeText, EncryptedValue: cipher, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = db.CreateProject(ctx, core.Project{ID: "retained", Name: "Retained project", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	var hs []*tar.Header
	var contents [][]byte
	for _, name := range []string{"dispatch.db", "dispatch-master.key"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		hs = append(hs, &tar.Header{Name: "./" + name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data)), Uid: 123, Gid: 456})
		contents = append(contents, data)
	}
	hs = append(hs, &tar.Header{Name: "./analytics", Typeflag: tar.TypeDir, Mode: 0700}, &tar.Header{Name: "./analytics/history.parquet", Typeflag: tar.TypeReg, Mode: 0600, Size: 7})
	contents = append(contents, nil, []byte("history"))
	file := filepath.Join(t.TempDir(), "data.tar.gz")
	writeTestTar(t, file, hs, contents)
	return file
}
func exportFixture(t *testing.T) string {
	t.Helper()
	stage := t.TempDir()
	data, err := os.ReadFile(dataFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stage, "data.tar.gz"), data, 0600)
	os.WriteFile(filepath.Join(stage, "dispatch.env"), []byte("SECRET=literal$'value\n"), 0600)
	meta := exportManifest{Format: 1, Image: "example/dispatch@sha256:" + strings.Repeat("a", 64), Files: map[string]string{}}
	for _, name := range []string{"data.tar.gz", "dispatch.env"} {
		meta.Files[name], err = fileHash(filepath.Join(stage, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = saveJSON(filepath.Join(stage, "manifest.json"), meta); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "dispatch.tar.gz")
	if err = packExport(stage, archive); err != nil {
		t.Fatal(err)
	}
	return archive
}
func TestPortableArchiveRetainsDataAndChecksIntegrity(t *testing.T) {
	archive := exportFixture(t)
	stage := t.TempDir()
	meta, err := unpackExport(archive, stage)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Format != 1 {
		t.Fatal("format")
	}
	env, _ := os.ReadFile(filepath.Join(stage, "dispatch.env"))
	if string(env) != "SECRET=literal$'value\n" {
		t.Fatal("environment changed")
	}
	info, _ := os.Stat(archive)
	if info.Mode().Perm() != 0600 {
		t.Fatal("archive is not private")
	}
	source := filepath.Join(stage, "data.tar.gz")
	sanitized := filepath.Join(stage, "restore.tar.gz")
	if err = validateData(source, sanitized); err != nil {
		t.Fatal(err)
	}
	before, _ := fileHash(source)
	if err = verifyExportDatabase(context.Background(), sanitized); err != nil {
		t.Fatal(err)
	}
	after, _ := fileHash(source)
	if before != after {
		t.Fatal("verification modified source")
	}
	f, _ := os.Open(sanitized)
	defer f.Close()
	z, _ := gzip.NewReader(f)
	defer z.Close()
	r := tar.NewReader(z)
	found := false
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Uid != 65532 || h.Gid != 65532 {
			t.Fatal("ownership not portable")
		}
		if h.Name == "analytics/history.parquet" {
			raw, _ := io.ReadAll(r)
			found = string(raw) == "history"
		}
	}
	if !found {
		t.Fatal("analytics omitted")
	}
	if err = packExport(stage, archive); err == nil {
		t.Fatal("overwrote existing export")
	}
	// Corruption in the outer gzip stream must not be accepted.
	raw, _ := os.ReadFile(archive)
	raw[len(raw)-5] ^= 0xff
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	os.WriteFile(bad, raw, 0600)
	if _, err = unpackExport(bad, t.TempDir()); err == nil {
		t.Fatal("gzip corruption accepted")
	}
}
func TestDataArchiveRejectsUnsafePathsAndLinks(t *testing.T) {
	cases := map[string][]*tar.Header{
		"escape":           {{Name: "../escape", Typeflag: tar.TypeReg}},
		"absolute":         {{Name: "/outside", Typeflag: tar.TypeReg}},
		"symlink escape":   {{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "../outside"}},
		"symlink parent":   {{Name: "a", Typeflag: tar.TypeSymlink, Linkname: "b"}, {Name: "a/file", Typeflag: tar.TypeReg}},
		"file parent":      {{Name: "a", Typeflag: tar.TypeReg}, {Name: "a/file", Typeflag: tar.TypeReg}},
		"hardlink missing": {{Name: "a", Typeflag: tar.TypeLink, Linkname: "missing"}},
		"device":           {{Name: "device", Typeflag: tar.TypeChar}},
		"duplicate":        {{Name: "a", Typeflag: tar.TypeReg}, {Name: "./a", Typeflag: tar.TypeReg}},
		"missing database": {{Name: "dispatch-master.key", Typeflag: tar.TypeReg}},
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTestTar(t, file, headers, make([][]byte, len(headers)))
			if err := validateData(file, ""); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}
func TestExportRestartsSourceAfterSnapshotFailure(t *testing.T) {
	m, _, calls := fixture(t)
	run := m.Run
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		line := strings.Join(args, " ")
		if strings.Contains(line, ".RepoDigests") {
			return "[]", nil
		}
		if strings.Contains(line, "tar -czf") {
			return "", errors.New("disk full")
		}
		return run(ctx, name, args...)
	}
	if err := m.Export(context.Background(), filepath.Join(t.TempDir(), "export.tar.gz")); err == nil {
		t.Fatal("snapshot failure ignored")
	}
	if !has(*calls, "stop --timeout") || !has(*calls, "up --detach") {
		t.Fatal("source was not restarted")
	}
	if _, err := os.Stat(m.path("export.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("export journal retained after successful restart")
	}
}
func importFixture(t *testing.T) (*Manager, Config, *[]string) {
	t.Helper()
	m, c, calls := fixture(t)
	for _, name := range []string{"installation.json", "compose.json", "dispatch.env"} {
		os.Remove(m.path(name))
	}
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	c.Socket = socket
	c.ID = ""
	c.Image = ""
	c.Volume = "portable-data"
	c.Name = "portable"
	c.Port = 18090
	c.PublicURL = "https://new.example.com"
	run := m.Run
	owned := ""
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.Contains(line, "context inspect"):
			return "unix://" + socket, nil
		case strings.Contains(line, "volume create"):
			for _, arg := range args {
				if strings.HasPrefix(arg, managedLabel+"=") {
					owned = strings.TrimPrefix(arg, managedLabel+"=")
				}
			}
			return "", nil
		case strings.Contains(line, "volume inspect"), strings.Contains(line, ".Config.Labels"):
			return owned, nil
		default:
			return run(ctx, name, args...)
		}
	}
	return m, c, calls
}
func TestImportFailureCanRecoverWithoutStartingEmptyController(t *testing.T) {
	archive := exportFixture(t)
	m, c, calls := importFixture(t)
	run := m.Run
	fail := true
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "tar -xzf /backup/restore.tar.gz") && fail {
			return "", errors.New("write failed")
		}
		return run(ctx, name, args...)
	}
	if err := m.Import(context.Background(), archive, c, false); err == nil {
		t.Fatal("failure ignored")
	}
	if has(*calls, "up --detach") {
		t.Fatal("started an incomplete import")
	}
	installed, err := m.Load()
	if err != nil || installed.ImportArchive == "" {
		t.Fatal("missing recovery seed")
	}
	if _, err = os.Stat(installed.ImportArchive); err != nil {
		t.Fatal("seed deleted")
	}
	if err = m.Upgrade(context.Background(), "", false, false); err == nil {
		t.Fatal("upgrade bypassed import recovery")
	}
	fail = false
	if err = m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	installed, err = m.Load()
	if err != nil || installed.ImportArchive != "" {
		t.Fatal("pending import not cleared")
	}
	if installed.Name != "portable" || installed.PublicURL != c.PublicURL || installed.Port != c.Port || installed.ID == "owned" {
		t.Fatal("host settings or installation identity were copied")
	}
	if !has(*calls, "up --detach") {
		t.Fatal("restored installation not started")
	}
	env, _ := os.ReadFile(m.path("dispatch.env"))
	if string(env) != "SECRET=literal$'value\n" {
		t.Fatal("environment lost")
	}
	if err = m.Import(context.Background(), archive, c, false); err == nil {
		t.Fatal("overwrote installed controller")
	}
}
func TestInvalidExportDoesNotTouchDockerOrInstallation(t *testing.T) {
	archive := exportFixture(t)
	stage := t.TempDir()
	meta, err := unpackExport(archive, stage)
	if err != nil {
		t.Fatal(err)
	}
	meta.Files["dispatch.env"] = "wrong"
	saveJSON(filepath.Join(stage, "manifest.json"), meta)
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	if err = packExport(stage, bad); err != nil {
		t.Fatal(err)
	}
	m, c, calls := importFixture(t)
	if err = m.Import(context.Background(), bad, c, false); err == nil {
		t.Fatal("checksum mismatch accepted")
	}
	if len(*calls) > 0 {
		t.Fatal("Docker touched before validation")
	}
	if _, err = os.Stat(m.path("installation.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("installation created")
	}
}
func TestInterruptedExportRecovery(t *testing.T) {
	m, c, calls := fixture(t)
	if err := saveJSON(m.path("export.json"), c); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !has(*calls, "up --detach") {
		t.Fatal("controller not restarted")
	}
}
func TestExportManifestRejectsUnsupportedVersion(t *testing.T) {
	archive := exportFixture(t)
	stage := t.TempDir()
	meta, err := unpackExport(archive, stage)
	if err != nil {
		t.Fatal(err)
	}
	meta.Format = 99
	saveJSON(filepath.Join(stage, "manifest.json"), meta)
	bad := filepath.Join(t.TempDir(), "bad.tar.gz")
	packExport(stage, bad)
	if _, err = unpackExport(bad, t.TempDir()); err == nil {
		t.Fatal("unsupported version accepted")
	}
}
func TestPortableCLIHelp(t *testing.T) {
	for _, command := range []string{"export", "import"} {
		var out bytes.Buffer
		handled, err := Run(context.Background(), []string{command, "--help"}, &out)
		if !handled || err != nil || !strings.Contains(out.String(), "archive") {
			t.Fatal("missing CLI options")
		}
	}
}
func TestVaultMismatchIsRejected(t *testing.T) {
	source := dataFixture(t)
	f, _ := os.Open(source)
	defer f.Close()
	z, _ := gzip.NewReader(f)
	defer z.Close()
	r := tar.NewReader(z)
	var headers []*tar.Header
	var content [][]byte
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(r)
		if strings.TrimPrefix(h.Name, "./") == "dispatch-master.key" {
			raw = bytes.Repeat([]byte{'x'}, 32)
			h.Size = int64(len(raw))
		}
		headers = append(headers, h)
		content = append(content, raw)
	}
	bad := filepath.Join(t.TempDir(), "wrong-key.tar.gz")
	writeTestTar(t, bad, headers, content)
	if err := verifyExportDatabase(context.Background(), bad); err == nil {
		t.Fatal("mismatched vault accepted")
	}
}

func TestArchiveTailIsBounded(t *testing.T) {
	if err := finishArchive(strings.NewReader(strings.Repeat("\x00", (1<<20)+1))); err == nil {
		t.Fatal("excessive padding accepted")
	}
	if err := finishArchive(strings.NewReader(strings.Repeat("\x00", 10240))); err != nil {
		t.Fatal(err)
	}
}

func TestTransferHelperOwnershipAndCancellation(t *testing.T) {
	for _, owned := range []bool{true, false} {
		t.Run(fmt.Sprint(owned), func(t *testing.T) {
			m, c, _ := fixture(t)
			calls := []string{}
			m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
				line := strings.Join(args, " ")
				calls = append(calls, line)
				switch {
				case strings.HasPrefix(line, "ps --all"):
					return "orphan", nil
				case strings.Contains(line, ".Config.Labels"):
					if owned {
						return c.ID, nil
					}
					return "foreign", nil
				case strings.HasPrefix(line, "run "):
					return "", context.Canceled
				default:
					return "", nil
				}
			}
			if err := m.transfer(context.Background(), c, "import", "--entrypoint", "sh", c.ImageID); err == nil {
				t.Fatal("failure ignored")
			}
			removed := has(calls, "rm --force orphan")
			if owned && !removed {
				t.Fatal("orphan left running")
			}
			if !owned && removed {
				t.Fatal("foreign container removed")
			}
			if owned && len(calls) < 7 {
				t.Fatal("failed helper was not cleaned up")
			}
		})
	}
}

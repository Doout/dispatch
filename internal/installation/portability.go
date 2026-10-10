package installation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Archives contain private configuration and the complete stopped data volume.
// Host-specific installation settings are deliberately not restored.
type exportManifest struct {
	Format    int               `json:"format"`
	CreatedAt time.Time         `json:"createdAt"`
	Image     string            `json:"image"`
	Files     map[string]string `json:"files"`
}

func (m *Manager) Export(ctx context.Context, output string) (err error) {
	if output == "" {
		return errors.New("provide --output for the export archive")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if _, err = os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		return errors.New("export destination already exists")
	}
	c, err := m.Load()
	if err != nil {
		return err
	}
	if err = m.preflight(ctx, c.Socket); err != nil {
		return err
	}
	if err = m.noPendingTransfer(); err != nil {
		return err
	}
	if err = m.checkManaged(ctx, c); err != nil {
		return err
	}
	if err = m.waitHealthy(ctx, c); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(m.Dir, ".export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	manifest := exportManifest{Format: 1, CreatedAt: time.Now().UTC(), Image: c.Image, Files: map[string]string{}}
	digests, err := m.Run(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", c.ImageID)
	if err != nil {
		return err
	}
	var refs []string
	if err = json.Unmarshal([]byte(digests), &refs); err != nil {
		return errors.New("could not read image digests")
	}
	if len(refs) > 0 {
		manifest.Image = refs[0]
	}
	env, err := os.ReadFile(m.path("dispatch.env"))
	if err != nil {
		return err
	}
	if len(env) > 1<<20 {
		return errors.New("controller settings exceed 1 MiB")
	}
	if err = writeFile(filepath.Join(stage, "dispatch.env"), env, 0600); err != nil {
		return err
	}
	// A journal makes a killed export recoverable, just like an interrupted upgrade.
	if err = saveJSON(m.path("export.json"), c); err != nil {
		return err
	}
	restarted := false
	restart := func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if recovery := m.start(cleanup, c); recovery != nil {
			return fmt.Errorf("controller restart failed; run dispatch recover: %w", recovery)
		}
		if removeErr := os.Remove(m.path("export.json")); removeErr != nil {
			return removeErr
		}
		restarted = true
		return nil
	}
	defer func() {
		if !restarted {
			err = errors.Join(err, restart())
		}
	}()
	fmt.Fprintln(m.Out, "Stopping Dispatch to export its data. Wait for active deployments to finish before moving the installation.")
	if _, err = m.compose(ctx, c, "stop", "--timeout", "30", "dispatch"); err != nil {
		return err
	}
	if err = m.transfer(ctx, c, "export", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", c.Volume+":/data:ro", "--volume", stage+":/backup", c.ImageID, "-c", fmt.Sprintf("umask 077; tar -czf /backup/data.tar.gz -C /data . && chown %d:%d /backup/data.tar.gz && sync /backup/data.tar.gz", os.Getuid(), os.Getgid())); err != nil {
		return err
	}
	if err = restart(); err != nil {
		return err
	}
	if err = validateData(filepath.Join(stage, "data.tar.gz"), ""); err != nil {
		return err
	}
	if err = verifyExportDatabase(ctx, filepath.Join(stage, "data.tar.gz")); err != nil {
		return err
	}
	for _, name := range []string{"dispatch.env", "data.tar.gz"} {
		manifest.Files[name], err = fileHash(filepath.Join(stage, name))
		if err != nil {
			return err
		}
	}
	if err = saveJSON(filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return err
	}
	// Publish without replacing an existing path, including one created concurrently.
	if err = packExport(stage, output); err != nil {
		return err
	}
	fmt.Fprintln(m.Out, "Export saved to", output)
	return nil
}

func (m *Manager) noPendingTransfer() error {
	if c, err := m.Load(); err == nil && c.ImportArchive != "" {
		return errors.New("an interrupted import exists; run dispatch recover first")
	}
	for _, name := range []string{"upgrade.json", "export.json"} {
		if _, err := os.Lstat(m.path(name)); !errors.Is(err, os.ErrNotExist) {
			return errors.New("an interrupted operation exists; run dispatch recover first")
		}
	}
	return nil
}

func (m *Manager) Import(ctx context.Context, input string, c Config, pull bool) error {
	if input == "" {
		return errors.New("provide --input for the export archive")
	}
	if err := m.noPendingTransfer(); err != nil {
		return err
	}
	if _, err := os.Lstat(m.path("installation.json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("import requires a new installation directory")
	}
	stage, err := os.MkdirTemp(m.Dir, ".import-")
	if err != nil {
		return err
	}
	// Retain the seed only when installation has recorded it for crash recovery.
	defer func() {
		if installed, err := m.Load(); err != nil || installed.ImportArchive == "" {
			os.RemoveAll(stage)
		}
	}()
	meta, err := unpackExport(input, stage)
	if err != nil {
		return err
	}
	seed := filepath.Join(stage, "restore.tar.gz")
	if err = validateData(filepath.Join(stage, "data.tar.gz"), seed); err != nil {
		return err
	}
	if err = verifyExportDatabase(ctx, seed); err != nil {
		return err
	}
	if c.Image == "" {
		c.Image = meta.Image
	}
	return m.install(ctx, c, filepath.Join(stage, "dispatch.env"), pull, seed)
}

func (m *Manager) finishImport(ctx context.Context, c Config) error {
	seed := c.ImportArchive
	if seed == "" {
		return nil
	}
	var err error
	if filepath.Dir(filepath.Dir(seed)) != m.Dir || !strings.HasPrefix(filepath.Base(filepath.Dir(seed)), ".import-") || filepath.Base(seed) != "restore.tar.gz" {
		return errors.New("invalid import recovery path")
	}
	if err = m.verifyVolume(ctx, c); err != nil {
		return err
	}
	if _, err = m.compose(ctx, c, "stop", "--timeout", "30", "dispatch"); err != nil {
		return err
	}
	if err = m.transfer(ctx, c, "import", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", c.Volume+":/data", "--volume", filepath.Dir(seed)+":/backup:ro", c.ImageID, "-c", "set -eu; find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; tar -xzf /backup/restore.tar.gz -C /data; chown 65532:65532 /data; sync"); err != nil {
		return err
	}
	c.ImportArchive = ""
	if err = saveJSON(m.path("installation.json"), c); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Dir(seed))
}

// Docker helper containers can outlive a killed CLI. Fence a previous helper
// before retrying, and stop the current helper if its Docker client fails.
func (m *Manager) transfer(ctx context.Context, c Config, kind string, args ...string) error {
	if err := m.cleanupTransfer(ctx, c, kind); err != nil {
		return err
	}
	prefix := []string{"run", "--rm", "--name", c.Name + "-" + kind, "--label", managedLabel + "=" + c.ID}
	_, err := m.Run(ctx, "docker", append(prefix, args...)...)
	if err == nil {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return errors.Join(err, m.cleanupTransfer(cleanup, c, kind))
}
func (m *Manager) cleanupTransfer(ctx context.Context, c Config, kind string) error {
	ids, err := m.Run(ctx, "docker", "ps", "--all", "--filter", "name=^/"+regexp.QuoteMeta(c.Name+"-"+kind)+"$", "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if ids == "" {
		return nil
	}
	if len(strings.Fields(ids)) != 1 {
		return errors.New("expected one transfer helper")
	}
	label, err := m.Run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "`+managedLabel+`"}}`, ids)
	if err != nil {
		return err
	}
	if label != c.ID {
		return errors.New("transfer helper name belongs to another container")
	}
	_, err = m.Run(ctx, "docker", "rm", "--force", ids)
	return err
}

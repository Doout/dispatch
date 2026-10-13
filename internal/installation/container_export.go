package installation

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const portableMountRoot = ".dispatch-export-mounts"
const exportDirectoryLabel = "io.dispatch.export-directory"

// DataMount restores a configuration directory at its original container path.
// The contents live in the imported data volume, never at an archived host path.
type DataMount struct {
	Target   string `json:"target"`
	Subpath  string `json:"subpath"`
	ReadOnly bool   `json:"readOnly"`
}

func validateDataMounts(mounts []DataMount) error {
	if len(mounts) > 32 {
		return errors.New("exports support at most 32 configuration directory mounts")
	}
	for i, mount := range mounts {
		if mount.Subpath != fmt.Sprintf("%s/%d", portableMountRoot, i) || !path.IsAbs(mount.Target) || path.Clean(mount.Target) != mount.Target || len(mount.Target) > 1024 || strings.ContainsAny(mount.Target, ":,$\n\r\\\x00") {
			return errors.New("invalid portable configuration mount")
		}
		for _, reserved := range []string{"/", "/data", "/proc", "/sys", "/dev", "/run", "/var/run", "/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/var/lib"} {
			if mount.Target == reserved || reserved != "/" && strings.HasPrefix(mount.Target, reserved+"/") {
				return errors.New("configuration mounts cannot replace controller data or system paths")
			}
		}
		for _, previous := range mounts[:i] {
			if mount.Target == previous.Target || strings.HasPrefix(mount.Target, previous.Target+"/") || strings.HasPrefix(previous.Target, mount.Target+"/") {
				return errors.New("configuration mount destinations overlap")
			}
		}
	}
	return nil
}

func validateArchiveMounts(archive string, mounts []DataMount) error {
	if err := validateDataMounts(mounts); err != nil || len(mounts) == 0 {
		return err
	}
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
	found := map[string]bool{}
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			found[strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")] = true
		}
	}
	for _, mount := range mounts {
		if !found[mount.Subpath] {
			return errors.New("export is missing a configuration directory")
		}
	}
	return finishArchive(z)
}

type containerMount struct {
	Type, Name, Source, Destination string
	RW                              bool
}
type exportContainer struct {
	ID, Image string
	Config    struct {
		Image string
		Env   []string
	}
	State struct {
		Running, Paused bool
	}
	Mounts          []containerMount
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func (m *Manager) inspectExportContainer(ctx context.Context, id string) (exportContainer, error) {
	var c exportContainer
	if id == "" || strings.HasPrefix(id, "-") || strings.ContainsAny(id, " \n\r\x00") {
		return c, errors.New("provide one Docker controller container name or ID")
	}
	raw, err := m.Run(ctx, "docker", "inspect", "--type", "container", id)
	if err != nil {
		return c, err
	}
	var containers []exportContainer
	if err = json.Unmarshal([]byte(raw), &containers); err != nil || len(containers) != 1 || len(containers[0].ID) != 64 {
		return c, errors.New("could not read controller container metadata")
	}
	return containers[0], nil
}

func exportContainerConfig(c exportContainer) (Config, containerMount, []containerMount, []byte, error) {
	if len(c.ID) < 12 {
		return Config{}, containerMount{}, nil, nil, errors.New("invalid controller container identity")
	}
	config := Config{ID: c.ID, Name: "dispatch-copy-" + c.ID[:12], Image: c.Config.Image, ImageID: c.Image, Bind: "127.0.0.1"}
	env := map[string]string{}
	for _, entry := range c.Config.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" || strings.ContainsAny(entry, "\r\n\x00") {
			return config, containerMount{}, nil, nil, errors.New("container environment must contain literal single-line KEY=value settings")
		}
		env[key] = value
	}
	if env["DATABASE_URL"] != "/data/dispatch.db" || env["DISPATCH_MASTER_KEY_FILE"] != "/data/dispatch-master.key" || env["DISPATCH_INSTANCE_CONNECTION_FILE"] != "" && env["DISPATCH_INSTANCE_CONNECTION_FILE"] != "/data/instance-connection.json" {
		return config, containerMount{}, nil, nil, errors.New("container export requires SQLite and the master key and connection file in /data; external PostgreSQL requires a separate backup")
	}
	var data containerMount
	var extra []containerMount
	for _, mount := range c.Mounts {
		if strings.ContainsAny(mount.Source+mount.Name, ",\r\n\x00") || mount.Type != "bind" && mount.Type != "volume" {
			return config, data, nil, nil, errors.New("unsupported controller mount; use a named volume or configuration directory bind")
		}
		if mount.Destination == "/var/run/docker.sock" {
			config.Socket = mount.Source
			continue
		}
		if mount.Destination == "/data" {
			data = mount
			continue
		}
		extra = append(extra, mount)
		config.DataMounts = append(config.DataMounts, DataMount{Target: mount.Destination, Subpath: fmt.Sprintf("%s/%d", portableMountRoot, len(extra)-1), ReadOnly: !mount.RW})
	}
	if data.Source == "" || config.Socket == "" {
		return config, data, nil, nil, errors.New("container must mount its /data directory and local Docker socket")
	}
	if err := validateDataMounts(config.DataMounts); err != nil {
		return config, data, nil, nil, err
	}
	bindings := c.NetworkSettings.Ports["8080/tcp"]
	if len(bindings) == 0 {
		return config, data, nil, nil, errors.New("container export requires a local published HTTP port for startup verification")
	}
	config.Bind = bindings[0].HostIP
	config.Port, _ = strconv.Atoi(bindings[0].HostPort)
	if config.Port < 1 || config.Port > 65535 {
		return config, data, nil, nil, errors.New("invalid published controller port")
	}
	literal := []byte(strings.Join(c.Config.Env, "\n") + "\n")
	if len(literal) > 1<<20 {
		return config, data, nil, nil, errors.New("controller settings exceed 1 MiB")
	}
	return config, data, extra, literal, nil
}

func readonlyMount(mount containerMount, target string) string {
	source := mount.Source
	if mount.Type == "volume" {
		source = mount.Name
	}
	return "type=" + mount.Type + ",source=" + source + ",target=" + target + ",readonly"
}

// ExportContainer snapshots an existing controller without adopting its Compose
// project or volume. It requires local Docker administrator access.
func (m *Manager) ExportContainer(ctx context.Context, id, output string) (err error) {
	if output == "" {
		return errors.New("provide --output for the export archive")
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if _, e := os.Lstat(output); !errors.Is(e, os.ErrNotExist) {
		return errors.New("export destination already exists")
	}
	if err = m.noPendingTransfer(); err != nil {
		return err
	}
	c, err := m.inspectExportContainer(ctx, id)
	if err != nil {
		return err
	}
	if !c.State.Running || c.State.Paused {
		return errors.New("start the controller before exporting")
	}
	config, data, extra, env, err := exportContainerConfig(c)
	if err != nil {
		return err
	}
	if err = m.preflight(ctx, config.Socket); err != nil {
		return err
	}
	if err = m.waitHealthy(ctx, config); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(m.Dir, ".export-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = writeFile(filepath.Join(stage, "dispatch.env"), env, 0600); err != nil {
		return err
	}
	args := []string{"--network", "none", "--user", "0", "--entrypoint", "sh", "--mount", readonlyMount(data, "/source"), "--volume", stage + ":/backup"}
	script := fmt.Sprintf("set -eu; umask 077; trap 'chown -R %d:%d /backup' EXIT; test ! -e /source/%s; ", os.Getuid(), os.Getgid(), portableMountRoot)
	for i, mount := range extra {
		args = append(args, "--mount", readonlyMount(mount, fmt.Sprintf("/input/%d", i)))
		script += fmt.Sprintf("test -d /input/%d; mkdir -p /backup/%s; cp -a /input/%d /backup/%s/%d; ", i, portableMountRoot, i, portableMountRoot, i)
	}
	script += "tar -I 'gzip -1' -cf /backup/data.tar.gz -C /source ."
	if len(extra) > 0 {
		script += " -C /backup " + portableMountRoot
	}
	script += fmt.Sprintf("; chown %d:%d /backup/data.tar.gz; sync /backup/data.tar.gz", os.Getuid(), os.Getgid())
	args = append(args, c.Image, "-c", script)
	if err = saveJSON(m.path("container-export.json"), config); err != nil {
		return err
	}
	// Docker reserves one name per controller, including across CLI directories.
	// The stopped marker has no mounts and prevents another export restarting the
	// source while this snapshot is being copied.
	if _, err = m.Run(ctx, "docker", "create", "--name", config.Name+"-export-lock", "--label", managedLabel+"="+config.ID, "--label", exportDirectoryLabel+"="+m.Dir, "--network", "none", "--entrypoint", "/bin/true", c.Image); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		owner, inspectErr := m.Run(cleanup, "docker", "inspect", "--format", `{{index .Config.Labels "`+exportDirectoryLabel+`"}}`, config.Name+"-export-lock")
		if inspectErr == nil && owner != m.Dir {
			return errors.Join(errors.New("another container export is active"), err, os.Remove(m.path("container-export.json")))
		}
		// A lost Docker reply can still have created the marker. Keep the journal
		// so recovery can release it once the engine is available again.
		return errors.Join(errors.New("container export lock could not be confirmed; run dispatch recover before retrying"), err)
	}
	restarted := false
	defer func() {
		if !restarted {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			_, e := m.recoverContainerExport(cleanup)
			err = errors.Join(err, e)
		}
	}()
	fmt.Fprintln(m.Out, "Stopping the controller to export data and configuration. Wait for active deployments to finish before moving it.")
	if _, err = m.Run(ctx, "docker", "stop", "--time", "30", c.ID); err != nil {
		return err
	}
	if err = m.transfer(ctx, config, "export", args...); err != nil {
		return err
	}
	if _, err = m.recoverContainerExport(ctx); err != nil {
		return err
	}
	restarted = true
	archive := filepath.Join(stage, "data.tar.gz")
	if err = validateData(archive, ""); err != nil {
		return err
	}
	if err = validateArchiveMounts(archive, config.DataMounts); err != nil {
		return err
	}
	if err = verifyExportDatabase(ctx, archive); err != nil {
		return err
	}
	manifest := exportManifest{Format: 1, CreatedAt: time.Now().UTC(), Image: c.Config.Image, Files: map[string]string{}, Mounts: config.DataMounts}
	if len(manifest.Mounts) > 0 {
		manifest.Format = 2
	}
	raw, err := m.Run(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", c.Image)
	if err != nil {
		return err
	}
	var digests []string
	if err = json.Unmarshal([]byte(raw), &digests); err != nil {
		return err
	}
	if len(digests) > 0 {
		manifest.Image = digests[0]
	}
	if !imagePattern.MatchString(manifest.Image) {
		return errors.New("invalid exported controller image reference")
	}
	for _, name := range []string{"data.tar.gz", "dispatch.env"} {
		manifest.Files[name], err = fileHash(filepath.Join(stage, name))
		if err != nil {
			return err
		}
	}
	if err = saveJSON(filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return err
	}
	if err = packExport(stage, output); err == nil {
		fmt.Fprintln(m.Out, "Export saved to", output)
	}
	return err
}

func (m *Manager) recoverContainerExport(ctx context.Context) (bool, error) {
	raw, err := os.ReadFile(m.path("container-export.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	var c Config
	if err = json.Unmarshal(raw, &c); err != nil || len(c.ID) != 64 || c.Name != "dispatch-copy-"+c.ID[:12] {
		return true, errors.New("invalid container export recovery journal")
	}
	if err = m.preflight(ctx, c.Socket); err != nil {
		return true, err
	}
	owner, err := m.Run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "`+exportDirectoryLabel+`"}}`, c.Name+"-export-lock")
	if err != nil || owner != m.Dir {
		current, inspectErr := m.inspectExportContainer(ctx, c.ID)
		if inspectErr == nil && current.State.Running && current.Image == c.ImageID {
			// No action on a controller protected by somebody else's lock. A
			// journal created before the lock attempt can simply be discarded.
			return true, os.Remove(m.path("container-export.json"))
		}
		return true, errors.New("export lock is missing or belongs to another directory; inspect the original controller before repairing this journal")
	}
	if err = m.cleanupTransfer(ctx, c, "export"); err != nil {
		return true, err
	}
	current, err := m.inspectExportContainer(ctx, c.ID)
	if err != nil || current.Image != c.ImageID {
		return true, errors.New("exported container changed; restart it manually before removing the recovery journal")
	}
	if _, err = m.Run(ctx, "docker", "start", c.ID); err != nil {
		return true, err
	}
	if err = m.waitHealthy(ctx, c); err != nil {
		return true, err
	}
	if _, err = m.Run(ctx, "docker", "rm", c.Name+"-export-lock"); err != nil {
		return true, err
	}
	return true, os.Remove(m.path("container-export.json"))
}

func (m *Manager) VerifyExport(ctx context.Context, input string) error {
	if input == "" {
		return errors.New("provide --input for the export archive")
	}
	stage, err := os.MkdirTemp(m.Dir, ".verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	manifest, err := unpackExport(input, stage)
	if err != nil {
		return err
	}
	archive := filepath.Join(stage, "data.tar.gz")
	if err = validateData(archive, ""); err != nil {
		return err
	}
	if err = validateArchiveMounts(archive, manifest.Mounts); err != nil {
		return err
	}
	if err = verifyExportDatabase(ctx, archive); err != nil {
		return err
	}
	fmt.Fprintln(m.Out, "Archive verified: database, master key, credentials and configuration directories are valid.")
	return nil
}

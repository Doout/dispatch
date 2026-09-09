// Package installation manages Docker controller installations independently of
// the controller process. No Kubernetes installer is included.
package installation

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const DefaultImage = "ghcr.io/doout/dispatch:stable"
const managedLabel = "io.dispatch.installation"

type Config struct {
	Schema       int       `json:"schema"`
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Image        string    `json:"image"`
	ImageID      string    `json:"imageId"`
	Bind         string    `json:"bind"`
	Port         int       `json:"port"`
	Volume       string    `json:"volume"`
	Socket       string    `json:"socket"`
	DockerGID    uint32    `json:"dockerGid"`
	PublicURL    string    `json:"publicUrl,omitempty"`
	ProxyNetwork string    `json:"proxyNetwork,omitempty"`
	Hostname     string    `json:"hostname,omitempty"`
	TLSResolver  string    `json:"tlsResolver,omitempty"`
	InstalledAt  time.Time `json:"installedAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}
type transaction struct {
	Previous Config `json:"previous"`
	Target   string `json:"target"`
	Backup   string `json:"backup"`
	Phase    string `json:"phase"`
}
type Runner func(context.Context, string, ...string) (string, error)
type Manager struct {
	Dir     string
	Run     Runner
	Out     io.Writer
	Health  func(context.Context, Config) error
	Timeout time.Duration
}

func Command(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}
func New(dir string) *Manager {
	return &Manager{Dir: dir, Run: Command, Out: os.Stdout, Timeout: 90 * time.Second}
}
func (m *Manager) Locked(fn func() error) error {
	if !filepath.IsAbs(m.Dir) {
		return errors.New("installation directory must be an absolute path")
	}
	if err := os.MkdirAll(m.Dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(m.Dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("installation directory must be a real directory writable only by its owner")
	}
	unlock, err := lockDirectory(m.Dir)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
func (m *Manager) path(name string) string { return filepath.Join(m.Dir, name) }
func writeFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".dispatch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func saveJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(data, '\n'), 0600)
}
func (m *Manager) Load() (Config, error) {
	var c Config
	data, err := os.ReadFile(m.path("installation.json"))
	if err == nil {
		err = json.Unmarshal(data, &c)
	}
	if err != nil {
		return c, fmt.Errorf("read managed installation: %w", err)
	}
	if c.Schema != 1 {
		return c, errors.New("unsupported installation format; update the CLI")
	}
	return c, nil
}
func (m *Manager) preflight(ctx context.Context, socket string) error {
	if host := os.Getenv("DOCKER_HOST"); host != "" && !strings.HasPrefix(host, "unix://") {
		return errors.New("the installer requires a local Docker engine")
	}
	if _, err := m.Run(ctx, "docker", "info", "--format", "{{.ServerVersion}}"); err != nil {
		return err
	}
	version, err := m.Run(ctx, "docker", "compose", "version", "--short")
	if err != nil {
		return fmt.Errorf("Docker Compose 2.30 or newer is required: %w", err)
	}
	var major, minor int
	fmt.Sscanf(strings.TrimPrefix(version, "v"), "%d.%d", &major, &minor)
	if major < 2 || major == 2 && minor < 30 {
		return fmt.Errorf("Docker Compose 2.30 or newer is required (found %s)", version)
	}
	host, err := m.Run(ctx, "docker", "context", "inspect", "--format", `{{(index .Endpoints "docker").Host}}`)
	if err != nil {
		return err
	}
	if os.Getenv("DOCKER_HOST") != "" {
		host = os.Getenv("DOCKER_HOST")
	}
	if !strings.HasPrefix(host, "unix://") {
		return errors.New("the selected Docker context is remote; use a local Docker engine")
	}
	if filepath.Clean(strings.TrimPrefix(host, "unix://")) != filepath.Clean(socket) {
		return errors.New("selected Docker engine does not match the installation socket; set DOCKER_HOST to unix://" + socket)
	}
	return nil
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,48}$`)
var imagePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/:@-]*$`)

func validate(c Config) error {
	if !namePattern.MatchString(c.Name) || !namePattern.MatchString(c.Volume) {
		return errors.New("name and volume must start with a lowercase letter and contain only letters, numbers, _ or -")
	}
	if !imagePattern.MatchString(c.Image) {
		return errors.New("invalid image reference")
	}
	if net.ParseIP(c.Bind) == nil || c.Port < 1 || c.Port > 65535 {
		return errors.New("bind must be an IP address and port must be between 1 and 65535")
	}
	if !filepath.IsAbs(c.Socket) || strings.ContainsAny(c.Socket, ":\n$") {
		return errors.New("Docker socket must be an absolute local path")
	}
	if c.ProxyNetwork == "" && (c.Hostname != "" || c.TLSResolver != "") {
		return errors.New("hostname and TLS resolver require --proxy-network")
	}
	if c.ProxyNetwork != "" {
		if !namePattern.MatchString(c.ProxyNetwork) || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.-]*$`).MatchString(c.Hostname) {
			return errors.New("proxy-network requires a valid hostname and network name")
		}
	}
	if c.TLSResolver != "" && !namePattern.MatchString(c.TLSResolver) {
		return errors.New("invalid TLS resolver")
	}
	if strings.ContainsAny(c.PublicURL, "\r\n$") {
		return errors.New("invalid public URL")
	}
	return nil
}
func (m *Manager) composeData(c Config) ([]byte, error) {

	service := map[string]any{
		"image":     c.ImageID,
		"restart":   "unless-stopped",
		"group_add": []string{fmt.Sprint(c.DockerGID)},
		"labels":    map[string]string{managedLabel: c.ID},
		"env_file":  []any{map[string]any{"path": m.path("dispatch.env"), "format": "raw"}},
		"environment": map[string]string{
			"DISPATCH_ADDR":             "0.0.0.0:8080",
			"DATABASE_URL":              "/data/dispatch.db",
			"DISPATCH_EXECUTOR":         "docker",
			"DISPATCH_MASTER_KEY_FILE":  "/data/dispatch-master.key",
			"DISPATCH_DOCKER_SOCKET":    "/var/run/docker.sock",
			"DISPATCH_REPOSITORY_CACHE": "/data/repositories",
			"DISPATCH_PUBLIC_URL":       c.PublicURL,
		},
		"ports":   []any{map[string]any{"target": 8080, "published": fmt.Sprint(c.Port), "host_ip": c.Bind}},
		"volumes": []string{"data:/data", c.Socket + ":/var/run/docker.sock"},
	}
	spec := map[string]any{
		"services": map[string]any{"dispatch": service},
		"volumes":  map[string]any{"data": map[string]any{"external": true, "name": c.Volume}},
	}

	if c.ProxyNetwork != "" {
		service["networks"] = []string{"default", "proxy"}
		spec["networks"] = map[string]any{"proxy": map[string]any{"external": true, "name": c.ProxyNetwork}}
		labels := service["labels"].(map[string]string)
		labels["traefik.enable"] = "true"
		labels["traefik.docker.network"] = c.ProxyNetwork
		labels["traefik.http.routers."+c.Name+".rule"] = "Host(`" + c.Hostname + "`)"
		labels["traefik.http.services."+c.Name+".loadbalancer.server.port"] = "8080"
		if c.TLSResolver != "" {
			labels["traefik.http.routers."+c.Name+".tls"] = "true"
			labels["traefik.http.routers."+c.Name+".tls.certresolver"] = c.TLSResolver
		}
	}
	return json.MarshalIndent(spec, "", "  ")
}
func (m *Manager) writeCompose(c Config) error {
	data, err := m.composeData(c)
	if err != nil {
		return err
	}
	return writeFile(m.path("compose.json"), data, 0600)
}
func (m *Manager) compose(ctx context.Context, c Config, args ...string) (string, error) {
	return m.Run(ctx, "docker", append([]string{"compose", "--project-name", c.Name, "--file", m.path("compose.json")}, args...)...)
}
func (m *Manager) resolve(ctx context.Context, image string, pull bool) (string, error) {
	if !imagePattern.MatchString(image) {
		return "", errors.New("invalid image reference")
	}
	if pull {
		fmt.Fprintln(m.Out, "Pulling", image)
		if _, err := m.Run(ctx, "docker", "pull", image); err != nil {
			return "", err
		}
	}
	id, err := m.Run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(id, "sha256:") {
		return "", errors.New("Docker did not return an image ID")
	}
	return id, nil
}
func (m *Manager) verifyVolume(ctx context.Context, c Config) error {
	label, err := m.Run(ctx, "docker", "volume", "inspect", "--format", `{{index .Labels "`+managedLabel+`"}}`, c.Volume)
	if err != nil {
		return err
	}
	if label != c.ID {
		return errors.New("data volume is not owned by this installation")
	}
	return nil
}
func (m *Manager) container(ctx context.Context, c Config) (string, error) {
	id, err := m.compose(ctx, c, "ps", "--all", "--quiet", "dispatch")
	if err != nil {
		return "", err
	}
	if id == "" || strings.Contains(id, "\n") {
		return "", errors.New("expected one managed controller container")
	}
	label, err := m.Run(ctx, "docker", "inspect", "--format", `{{index .Config.Labels "`+managedLabel+`"}}`, id)
	if err != nil {
		return "", err
	}
	if label != c.ID {
		return "", errors.New("controller is not owned by this installation")
	}
	return id, nil
}
func (m *Manager) waitHealthy(ctx context.Context, c Config) error {
	ctx, cancel := context.WithTimeout(ctx, m.Timeout)
	defer cancel()
	if m.Health != nil {
		return m.Health(ctx, c)
	}
	host := c.Bind
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	url := "http://" + net.JoinHostPort(host, fmt.Sprint(c.Port)) + "/healthz"
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := client.Do(req)
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("controller did not become healthy at %s: %w", url, ctx.Err())
		case <-ticker.C:
		}
	}
}
func (m *Manager) start(ctx context.Context, c Config) error {
	if err := m.writeCompose(c); err != nil {
		return err
	}
	if _, err := m.compose(ctx, c, "up", "--detach", "--force-recreate", "--no-build", "--pull", "never", "dispatch"); err != nil {
		return err
	}
	id, err := m.container(ctx, c)
	if err != nil {
		return err
	}
	image, err := m.Run(ctx, "docker", "inspect", "--format", "{{.Image}}", id)
	if err != nil {
		return err
	}
	if image != c.ImageID {
		return errors.New("controller is running an unexpected image")
	}
	return m.waitHealthy(ctx, c)
}
func (m *Manager) Install(ctx context.Context, c Config, envFile string, pull bool) error {
	if err := validate(c); err != nil {
		return err
	}
	if _, err := os.Stat(m.path("installation.json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("installation already exists; use dispatch upgrade or dispatch recover")
	}
	for _, name := range []string{"compose.json", "dispatch.env", "upgrade.json"} {
		if _, err := os.Lstat(m.path(name)); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("refusing to overwrite %s", m.path(name))
		}
	}
	if err := m.preflight(ctx, c.Socket); err != nil {
		return err
	}
	ids, err := m.Run(ctx, "docker", "ps", "-a", "--filter", "label=com.docker.compose.project="+c.Name, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if ids != "" {
		return errors.New("a Compose project with this name already exists; choose another --name")
	}
	volumes, err := m.Run(ctx, "docker", "volume", "ls", "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	for _, v := range strings.Fields(volumes) {
		if v == c.Volume {
			return errors.New("data volume already exists; refusing to adopt or overwrite it")
		}
	}
	if c.ProxyNetwork != "" {
		if _, err = m.Run(ctx, "docker", "network", "inspect", c.ProxyNetwork); err != nil {
			return err
		}
	}
	env := []byte("# Optional controller settings. Values are literal; do not add shell quotes.\n")
	if envFile != "" {
		env, err = os.ReadFile(envFile)
		if err != nil {
			return err
		}
	}
	gid, err := socketGroup(c.Socket)
	if err != nil {
		return err
	}
	c.DockerGID = gid
	c.ImageID, err = m.resolve(ctx, c.Image, pull)
	if err != nil {
		return err
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return err
	}
	c.ID = hex.EncodeToString(random)
	c.Schema = 1
	c.InstalledAt = time.Now().UTC()
	c.UpdatedAt = c.InstalledAt
	if err = writeFile(m.path("dispatch.env"), env, 0600); err != nil {
		return err
	}
	if err = m.writeCompose(c); err != nil {
		return err
	}
	if err = saveJSON(m.path("installation.json"), c); err != nil {
		return err
	}
	if err = m.initializeVolume(ctx, c); err != nil {
		return fmt.Errorf("installation retained; run dispatch recover --dir %s: %w", m.Dir, err)
	}
	if err = m.start(ctx, c); err != nil {
		return fmt.Errorf("installation retained for repair; run dispatch recover --dir %s: %w", m.Dir, err)
	}
	fmt.Fprintf(m.Out, "Dispatch is ready at http://%s. Complete owner setup in the browser.\n", net.JoinHostPort(c.Bind, fmt.Sprint(c.Port)))
	return nil
}
func (m *Manager) checkManaged(ctx context.Context, c Config) error {
	if err := m.verifyVolume(ctx, c); err != nil {
		return err
	}
	expected, err := m.composeData(c)
	if err != nil {
		return err
	}
	actual, err := os.ReadFile(m.path("compose.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("managed compose.json was modified; restore it before upgrading (put controller settings in dispatch.env)")
	}
	id, err := m.container(ctx, c)
	if err != nil {
		return err
	}
	image, err := m.Run(ctx, "docker", "inspect", "--format", "{{.Image}}", id)
	if err != nil {
		return err
	}
	if image != c.ImageID {
		return errors.New("controller image changed outside the installer")
	}
	return nil
}
func (m *Manager) Upgrade(ctx context.Context, image string, pull, check bool) error {
	c, err := m.Load()
	if err != nil {
		return err
	}
	if err := m.preflight(ctx, c.Socket); err != nil {
		return err
	}
	if _, err = os.Stat(m.path("upgrade.json")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("an interrupted upgrade exists; run dispatch recover before upgrading")
	}
	if err = m.checkManaged(ctx, c); err != nil {
		return err
	}
	if image == "" {
		image = c.Image
	}
	target, err := m.resolve(ctx, image, pull)
	if err != nil {
		return err
	}
	if check {
		fmt.Fprintf(m.Out, "Installed: %s\nAvailable: %s\nUpgrade available: %t\n", c.ImageID, target, c.ImageID != target)
		return nil
	}
	if c.ImageID == target {
		if image != c.Image {
			c.Image = image
			if err = saveJSON(m.path("installation.json"), c); err != nil {
				return err
			}
		}
		fmt.Fprintln(m.Out, "Dispatch is already up to date.")
		return nil
	}
	backup := m.path("backups/" + time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err = os.MkdirAll(backup, 0700); err != nil {
		return err
	}
	tx := transaction{Previous: c, Target: target, Backup: backup, Phase: "prepared"}
	if err = saveJSON(m.path("upgrade.json"), tx); err != nil {
		return err
	}
	if err = m.performUpgrade(ctx, c, image, &tx); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		recovery := m.recoverTransaction(cleanup, tx)
		if recovery != nil {
			return fmt.Errorf("upgrade failed: %w; recovery also failed: %v; run dispatch recover --dir %s", err, recovery, m.Dir)
		}
		return fmt.Errorf("upgrade failed; previous image and data restored: %w", err)
	}
	fmt.Fprintln(m.Out, "Dispatch upgraded. Backup:", backup)
	return nil
}
func (m *Manager) performUpgrade(ctx context.Context, c Config, image string, tx *transaction) error {
	fmt.Fprintln(m.Out, "Stopping Dispatch and backing up its data…")
	if _, err := m.compose(ctx, c, "stop", "--timeout", "30", "dispatch"); err != nil {
		return err
	}
	if _, err := m.Run(ctx, "docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", c.Volume+":/data:ro", "--volume", tx.Backup+":/backup", c.ImageID, "-c", "tar -czf /backup/data.tar.gz -C /data . && sync /backup/data.tar.gz"); err != nil {
		return err
	}
	if err := saveJSON(filepath.Join(tx.Backup, "installation.json"), c); err != nil {
		return err
	}
	tx.Phase = "replacing"
	if err := saveJSON(m.path("upgrade.json"), tx); err != nil {
		return err
	}
	c.Image = image
	c.ImageID = tx.Target
	c.UpdatedAt = time.Now().UTC()
	if err := m.start(ctx, c); err != nil {
		return err
	}
	if err := saveJSON(m.path("installation.json"), c); err != nil {
		return err
	}
	return os.Remove(m.path("upgrade.json"))
}
func (m *Manager) recoverTransaction(ctx context.Context, tx transaction) error {
	c := tx.Previous
	if err := m.verifyVolume(ctx, c); err != nil {
		return err
	}
	if tx.Phase == "replacing" {
		if _, err := m.compose(ctx, c, "stop", "--timeout", "30", "dispatch"); err != nil {
			return err
		}
		if _, err := m.Run(ctx, "docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", c.Volume+":/data", "--volume", tx.Backup+":/backup:ro", c.ImageID, "-c", `tar -tzf /backup/data.tar.gz >/dev/null && find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} + && tar -xzf /backup/data.tar.gz -C /data`); err != nil {
			return err
		}
	}
	if err := m.start(ctx, c); err != nil {
		return err
	}
	if err := saveJSON(m.path("installation.json"), c); err != nil {
		return err
	}
	return os.Remove(m.path("upgrade.json"))
}
func (m *Manager) Recover(ctx context.Context) error {
	installed, err := m.Load()
	if err != nil {
		return err
	}
	if err := m.preflight(ctx, installed.Socket); err != nil {
		return err
	}
	data, err := os.ReadFile(m.path("upgrade.json"))
	if errors.Is(err, os.ErrNotExist) {
		c, err := m.Load()
		if err != nil {
			return err
		}
		if err = m.initializeVolume(ctx, c); err != nil {
			return err
		}
		return m.start(ctx, c)
	}
	if err != nil {
		return err
	}
	var tx transaction
	if err = json.Unmarshal(data, &tx); err != nil {
		return err
	}
	if tx.Phase != "prepared" && tx.Phase != "replacing" {
		return errors.New("unrecognized upgrade phase")
	}
	c, err := m.Load()
	if err != nil {
		return err
	}
	if c.ImageID == tx.Target {
		if err = m.waitHealthy(ctx, c); err != nil {
			return err
		}
		return os.Remove(m.path("upgrade.json"))
	}
	return m.recoverTransaction(ctx, tx)
}
func (m *Manager) Status(ctx context.Context) error {
	c, err := m.Load()
	if err != nil {
		return err
	}
	fmt.Fprintf(m.Out, "Installation: %s\nImage: %s\nInstalled image: %s\nData volume: %s\n", c.Name, c.Image, c.ImageID, c.Volume)
	if _, err = os.Stat(m.path("upgrade.json")); err == nil {
		fmt.Fprintln(m.Out, "Interrupted upgrade: run dispatch recover")
	}
	_, err = m.container(ctx, c)
	if err != nil {
		return err
	}
	if err = m.waitHealthy(ctx, c); err != nil {
		return err
	}
	fmt.Fprintln(m.Out, "Health: ready")
	return nil
}

func (m *Manager) initializeVolume(ctx context.Context, c Config) error {
	if _, err := m.Run(ctx, "docker", "volume", "create", "--label", managedLabel+"="+c.ID, c.Volume); err != nil {
		return err
	}
	if err := m.verifyVolume(ctx, c); err != nil {
		return err
	}
	_, err := m.Run(ctx, "docker", "run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", c.Volume+":/data", c.ImageID, "-c", `set -eu
umask 077
if [ ! -e /data/dispatch-master.key ]; then
  if [ -e /data/dispatch.db ]; then echo 'Existing database has no master key; restore the original key.' >&2; exit 1; fi
  dd if=/dev/urandom of=/data/.dispatch-key.raw bs=32 count=1 2>/dev/null
  base64 /data/.dispatch-key.raw > /data/.dispatch-key.new
  rm /data/.dispatch-key.raw
  chown 65532:65532 /data/.dispatch-key.new
  mv /data/.dispatch-key.new /data/dispatch-master.key
fi
chown 65532:65532 /data`)
	return err
}

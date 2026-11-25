package installation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Manager, Config, *[]string) {
	t.Helper()
	t.Setenv("DOCKER_HOST", "")
	m := New(t.TempDir())
	if err := os.Chmod(m.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	m.Out = io.Discard
	m.Timeout = time.Second
	c := Config{Schema: 1, ID: "owned", Name: "dispatch-test", Image: "example/dispatch:stable", ImageID: "sha256:old", Bind: "127.0.0.1", Port: 18089, Volume: "dispatch-test-data", Socket: "/var/run/docker.sock"}
	if err := saveJSON(m.path("installation.json"), c); err != nil {
		t.Fatal(err)
	}
	if err := m.writeCompose(c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.path("dispatch.env"), []byte("SECRET=keep$literal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	running := c.ImageID
	m.Run = func(ctx context.Context, command string, args ...string) (string, error) {
		line := command + " " + strings.Join(args, " ")
		calls = append(calls, line)
		switch {
		case strings.Contains(line, "compose version"):
			return "2.39.1", nil
		case strings.Contains(line, "context inspect"):
			return "unix://" + c.Socket, nil
		case strings.Contains(line, "volume inspect"):
			return c.ID, nil
		case strings.Contains(line, "docker image inspect"):
			return "sha256:new", nil
		case strings.Contains(line, ".Config.Labels"):
			return c.ID, nil
		case strings.Contains(line, "{{.Image}}"):
			return running, nil
		case strings.Contains(line, " up "):
			data, _ := os.ReadFile(m.path("compose.json"))
			var spec struct {
				Services map[string]struct {
					Image string `json:"image"`
				} `json:"services"`
			}
			json.Unmarshal(data, &spec)
			running = spec.Services["dispatch"].Image
			return "", nil
		case strings.Contains(line, " ps --all --quiet"):
			return "controller", nil
		default:
			return "", nil
		}
	}
	m.Health = func(context.Context, Config) error { return nil }
	return m, c, &calls
}
func has(calls []string, text string) bool {
	for _, call := range calls {
		if strings.Contains(call, text) {
			return true
		}
	}
	return false
}
func TestUpgradeRollbackRestoresDataAndImage(t *testing.T) {
	m, c, calls := fixture(t)
	m.Health = func(_ context.Context, next Config) error {
		if next.ImageID == "sha256:new" {
			return errors.New("bad migration")
		}
		return nil
	}
	err := m.Upgrade(context.Background(), "", true, false)
	if err == nil || !strings.Contains(err.Error(), "previous image and data restored") {
		t.Fatalf("expected rollback, got %v", err)
	}
	if !has(*calls, "-czf /backup/data.tar.gz") || !has(*calls, "tar -xzf /backup/data.tar.gz") {
		t.Fatalf("missing backup/restore: %v", *calls)
	}
	restored, err := m.Load()
	if err != nil || restored.ImageID != c.ImageID {
		t.Fatalf("old image not restored: %v %#v", err, restored)
	}
	env, _ := os.ReadFile(m.path("dispatch.env"))
	if string(env) != "SECRET=keep$literal\n" {
		t.Fatal("environment changed")
	}
	if _, err := os.Stat(m.path("upgrade.json")); !os.IsNotExist(err) {
		t.Fatal("transaction not completed")
	}
}
func TestBackupFailureRestartsWithoutRestoringIncompleteArchive(t *testing.T) {
	m, _, calls := fixture(t)
	run := m.Run
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "-czf /backup/data.tar.gz") {
			return "", errors.New("disk full")
		}
		return run(ctx, name, args...)
	}
	if err := m.Upgrade(context.Background(), "", true, false); err == nil {
		t.Fatal("expected backup failure")
	}
	if has(*calls, "tar -xzf") {
		t.Fatal("restored incomplete backup")
	}
	if !has(*calls, " up ") {
		t.Fatal("old controller not restarted")
	}
}
func TestPullFailureDoesNotStopController(t *testing.T) {
	m, _, calls := fixture(t)
	run := m.Run
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "pull" {
			return "", errors.New("registry unavailable")
		}
		return run(ctx, name, args...)
	}
	if err := m.Upgrade(context.Background(), "", true, false); err == nil {
		t.Fatal("expected pull failure")
	}
	if has(*calls, " stop ") {
		t.Fatal("stopped controller before download succeeded")
	}
}
func TestUpgradeCommitAndCheck(t *testing.T) {
	m, _, calls := fixture(t)
	if err := m.Upgrade(context.Background(), "", true, true); err != nil {
		t.Fatal(err)
	}
	if has(*calls, " stop ") {
		t.Fatal("check stopped controller")
	}
	if err := m.Upgrade(context.Background(), "example/dispatch:v2", true, false); err != nil {
		t.Fatal(err)
	}
	c, err := m.Load()
	if err != nil || c.ImageID != "sha256:new" || c.Image != "example/dispatch:v2" {
		t.Fatal("new image was not committed")
	}
	*calls = nil
	if err = m.Upgrade(context.Background(), "", true, false); err != nil {
		t.Fatal(err)
	}
	if has(*calls, " stop ") {
		t.Fatal("unchanged image restarted")
	}
}
func TestInterruptedUpgradeCanRecover(t *testing.T) {
	m, c, calls := fixture(t)
	tx := transaction{Previous: c, Target: "sha256:new", Backup: m.path("backups/test"), Phase: "replacing"}
	if err := saveJSON(m.path("upgrade.json"), tx); err != nil {
		t.Fatal(err)
	}
	if err := m.Upgrade(context.Background(), "", false, false); err == nil {
		t.Fatal("overwrote pending transaction")
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !has(*calls, "tar -xzf") {
		t.Fatal("recovery did not restore data")
	}
}
func TestRefusesModifiedComposeAndForeignVolume(t *testing.T) {
	m, _, calls := fixture(t)
	os.WriteFile(m.path("compose.json"), []byte("{}"), 0600)
	if err := m.Upgrade(context.Background(), "", false, false); err == nil {
		t.Fatal("accepted modified compose")
	}
	if has(*calls, " stop ") {
		t.Fatal("stopped modified installation")
	}
	m, _, _ = fixture(t)
	run := m.Run
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if strings.Contains(strings.Join(args, " "), "volume inspect") {
			return "foreign", nil
		}
		return run(ctx, name, args...)
	}
	if err := m.Upgrade(context.Background(), "", false, false); err == nil {
		t.Fatal("accepted foreign data volume")
	}
}
func TestDirectoryLock(t *testing.T) {
	m, _, _ := fixture(t)
	err := m.Locked(func() error { return m.Locked(func() error { t.Fatal("acquired second lock"); return nil }) })
	if err == nil || !strings.Contains(err.Error(), "another installation command") {
		t.Fatal("missing lock conflict")
	}
}
func TestInstallPreservesRawEnvironmentAndDetectsSocketGroup(t *testing.T) {
	m, c, calls := fixture(t)
	for _, name := range []string{"installation.json", "compose.json", "dispatch.env"} {
		os.Remove(m.path(name))
	}
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	c.Socket = socket
	source := filepath.Join(t.TempDir(), "config")
	os.WriteFile(source, []byte("PASSWORD=literal$with'quotes\n"), 0600)
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
	if err = m.Install(context.Background(), c, source, false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(m.path("dispatch.env"))
	if string(data) != "PASSWORD=literal$with'quotes\n" {
		t.Fatal("environment was interpolated")
	}
	info, _ := os.Stat(m.path("dispatch.env"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("environment permissions")
	}
	if !has(*calls, "65532:65532 /data") {
		t.Fatal("data directory was not initialized")
	}
	content, _ := os.ReadFile(m.path("compose.json"))
	if !strings.Contains(string(content), `"format": "raw"`) {
		t.Fatal("missing raw env_file")
	}
	if err = m.Install(context.Background(), c, source, false); err == nil {
		t.Fatal("repeated install overwrote state")
	}
}
func TestAutoUpgradeUsesOwnedDockerContainer(t *testing.T) {
	m, c, calls := fixture(t)
	exists := false
	run := m.Run
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		line := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(line, "ps --all --filter name="):
			if exists {
				return "updater", nil
			}
			return "", nil
		case strings.Contains(line, updaterRoleLabel):
			if strings.HasPrefix(line, "inspect") {
				return c.ID + " updater", nil
			}
		case strings.Contains(line, "{{.State.Status}}"):
			return "running", nil
		}
		if strings.HasPrefix(line, "run --detach") {
			exists = true
		}
		if strings.HasPrefix(line, "rm updater") {
			exists = false
		}
		return run(ctx, name, args...)
	}
	if err := m.AutoUpgrade(context.Background(), "status", 24*time.Hour, true, ""); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("status enabled automatic updates")
	}
	if err := m.AutoUpgrade(context.Background(), "enable", 24*time.Hour, true, ""); err != nil {
		t.Fatal(err)
	}
	if !exists || !has(*calls, "--restart unless-stopped") || !has(*calls, "auto-upgrade run") {
		t.Fatalf("updater not created: %v", *calls)
	}
	if !has(*calls, "--volume "+m.Dir+":"+m.Dir) || !has(*calls, "--network host") {
		t.Fatal("updater cannot access host paths and health endpoint")
	}
	if has(*calls, "systemctl") {
		t.Fatal("used a host service")
	}
	if err := m.AutoUpgrade(context.Background(), "disable", 24*time.Hour, true, ""); err != nil {
		t.Fatal(err)
	}
	if exists || !has(*calls, "stop --time 180 updater") || !has(*calls, "rm updater") {
		t.Fatal("updater not stopped and removed")
	}
	if has(*calls, "stop --timeout 30 dispatch") {
		t.Fatal("disabling updater stopped controller")
	}
}
func TestAutomaticSchedulePersistsAndReleasesLock(t *testing.T) {
	m, _, calls := fixture(t)
	wait, err := m.automaticCheck(context.Background(), 24*time.Hour, true)
	if err != nil || wait < 24*time.Hour {
		t.Fatalf("first check: %v %s", err, wait)
	}
	if !has(*calls, "docker pull") {
		t.Fatal("did not check for an update")
	}
	*calls = nil
	if err = m.Locked(func() error { return nil }); err != nil {
		t.Fatal("worker retained lock between checks")
	}
	wait, err = m.automaticCheck(context.Background(), 24*time.Hour, true)
	if err != nil || wait <= 0 || has(*calls, "docker pull") {
		t.Fatal("restart did not respect persisted schedule")
	}
}
func TestAutomaticRecoveryPrecedesScheduledCheck(t *testing.T) {
	m, c, calls := fixture(t)
	saveJSON(m.path("upgrade.json"), transaction{Previous: c, Target: "sha256:new", Backup: m.path("backups/test"), Phase: "replacing"})
	saveJSON(m.path("automatic.json"), automaticSchedule{NextCheck: time.Now().Add(24 * time.Hour)})
	if _, err := m.automaticCheck(context.Background(), 24*time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if !has(*calls, "tar -xzf") || has(*calls, "docker pull") {
		t.Fatal("did not recover pending upgrade before waiting")
	}
}
func TestRefusesForeignUpdater(t *testing.T) {
	m, _, calls := fixture(t)
	run := m.Run
	m.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		if strings.HasPrefix(strings.Join(args, " "), "ps --all --filter name=") {
			return "foreign", nil
		}
		return run(ctx, name, args...)
	}
	if err := m.AutoUpgrade(context.Background(), "disable", 24*time.Hour, true, ""); err == nil {
		t.Fatal("accepted foreign updater")
	}
	if has(*calls, "docker stop") || has(*calls, "docker rm") {
		t.Fatal("modified foreign container")
	}
}
func TestCLIHelpAndServerCompatibility(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}} {
		handled, err := Run(context.Background(), args, io.Discard)
		if handled || err != nil {
			t.Fatal("server compatibility")
		}
	}
	var output bytes.Buffer
	handled, err := Run(context.Background(), []string{"install", "--help"}, &output)
	if !handled || err != nil || !strings.Contains(output.String(), "-image") {
		t.Fatalf("help: %v %s", err, output.String())
	}
	if _, err = Run(context.Background(), []string{"install", "--timeout", "0s"}, io.Discard); err == nil {
		t.Fatal("invalid timeout")
	}
}

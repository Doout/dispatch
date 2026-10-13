package installation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestComposeDockerInstallationExport(t *testing.T) {
	image := os.Getenv("DISPATCH_INSTALLATION_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_INSTALLATION_DOCKER_IMAGE to a local controller image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	name := fmt.Sprintf("compose-export-%d", time.Now().UnixNano())
	volume := name + "-data"
	configurationVolume := name + "-config"
	docker := func(args ...string) string {
		t.Helper()
		out, err := Command(ctx, "docker", args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	port := func() int {
		t.Helper()
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return l.Addr().(*net.TCPAddr).Port
	}
	sourcePort := port()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "cluster"), []byte("private configuration retained"), 0600); err != nil {
		t.Fatal(err)
	}
	docker("volume", "create", "--label", "dispatch.test=compose-export", volume)
	docker("volume", "create", "--label", "dispatch.test=compose-export", configurationVolume)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		Command(cleanup, "docker", "rm", "--force", name)
		Command(cleanup, "docker", "volume", "rm", volume)
		Command(cleanup, "docker", "volume", "rm", configurationVolume)
	})
	docker("run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "python3", "--volume", volume+":/data", image, "-c", "import base64,secrets,pathlib; p=pathlib.Path('/data/dispatch-master.key'); p.write_bytes(base64.b64encode(secrets.token_bytes(32))); p.chmod(0o600)")
	docker("run", "--rm", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", configurationVolume+":/config", image, "-c", "mkdir -p /config/nested; printf retained > /config/nested/secondary; printf omitted > /config/parent-only")
	docker("run", "--detach", "--name", name, "--label", "com.docker.compose.project="+name, "--user", "0", "--publish", fmt.Sprintf("127.0.0.1:%d:8080", sourcePort), "--volume", volume+":/data", "--volume", "/var/run/docker.sock:/var/run/docker.sock", "--mount", "type=bind,source="+directory+",target=/kubeconfigs,readonly", "--mount", "type=volume,source="+configurationVolume+",target=/extra-configs,volume-subpath=nested,readonly", "--env", "DATABASE_URL=/data/dispatch.db", "--env", "DISPATCH_MASTER_KEY_FILE=/data/dispatch-master.key", "--env", "DISPATCH_ADDR=0.0.0.0:8080", "--env", "DISPATCH_EXECUTOR=docker", "--env", "DISPATCH_DOCKER_SOCKET=/var/run/docker.sock", image)
	m := New(t.TempDir())
	if err := os.Chmod(m.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	m.Out = io.Discard
	if err := m.waitHealthy(ctx, Config{Bind: "127.0.0.1", Port: sourcePort}); err != nil {
		t.Fatal(err)
	}
	request := func(port int, method, route, body string, status int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d/api/v1%s", port, route), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth("portable-owner", "disposable-portability-password")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("%s %s status=%d err=%v", method, route, response.StatusCode, err)
		}
		return raw
	}
	request(sourcePort, "POST", "/auth/setup", `{"username":"portable-owner","password":"disposable-portability-password"}`, 201)
	created := request(sourcePort, "POST", "/projects", `{"name":"Compose project retained"}`, 201)
	request(sourcePort, "POST", "/secrets", `{"name":"Compose credential","type":"text","environmentVariable":"PORTABLE_TOKEN","value":"disposable encrypted value"}`, 201)
	var project struct{ ID string }
	if err := json.Unmarshal(created, &project); err != nil {
		t.Fatal(err)
	}
	key := docker("exec", name, "cat", "/data/dispatch-master.key")
	archive := filepath.Join(t.TempDir(), "copy.tar.gz")
	if err := m.Locked(func() error { return m.ExportContainer(ctx, name, archive) }); err != nil {
		t.Fatal(err)
	}
	request(sourcePort, "GET", "/projects", "", 200)
	docker("stop", name)
	target := New(t.TempDir())
	if err := os.Chmod(target.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	target.Out = io.Discard
	targetPort := port()
	c := Config{Name: name + "-copy", Volume: name + "-copy-data", Bind: "127.0.0.1", Port: targetPort, Socket: "/var/run/docker.sock"}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		target.compose(cleanup, c, "down", "--remove-orphans")
		if installed, err := target.Load(); err == nil && target.verifyVolume(cleanup, installed) == nil {
			Command(cleanup, "docker", "volume", "rm", c.Volume)
		}
	})
	if err := target.Locked(func() error { return target.Import(ctx, archive, c, false) }); err != nil {
		t.Fatal(err)
	}
	installed, err := target.Load()
	if err != nil || len(installed.DataMounts) != 2 {
		t.Fatal("configuration mount not restored", err)
	}
	id, err := target.container(ctx, installed)
	if err != nil {
		t.Fatal(err)
	}
	if docker("exec", id, "cat", "/data/dispatch-master.key") != key {
		t.Fatal("master key changed")
	}
	if docker("exec", id, "cat", "/kubeconfigs/cluster") != "private configuration retained" {
		t.Fatal("mounted configuration lost")
	}
	if docker("exec", id, "cat", "/extra-configs/secondary") != "retained" {
		t.Fatal("source configuration volume subpath lost")
	}
	if _, err = Command(ctx, "docker", "exec", id, "test", "-e", "/extra-configs/parent-only"); err == nil {
		t.Fatal("export copied data outside the mounted configuration subpath")
	}
	if _, err = Command(ctx, "docker", "exec", id, "sh", "-c", "echo changed >> /kubeconfigs/cluster"); err == nil {
		t.Fatal("read-only configuration became writable")
	}
	request(targetPort, "POST", "/auth/login", `{"username":"portable-owner","password":"disposable-portability-password"}`, 200)
	if !strings.Contains(string(request(targetPort, "GET", "/projects", "", 200)), project.ID) || !strings.Contains(string(request(targetPort, "GET", "/secrets", "", 200)), "Compose credential") {
		t.Fatal("accounts, projects or saved credentials lost")
	}
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err = target.Locked(func() error { return target.Export(ctx, second) }); err != nil {
		t.Fatal(err)
	}
	if err = target.VerifyExport(ctx, second); err != nil {
		t.Fatal(err)
	}
}

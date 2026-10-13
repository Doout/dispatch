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

// Opt in with a locally built controller image. Only uniquely named disposable
// Compose projects and volumes are created; no existing installation is used.
func TestPortableDockerInstallation(t *testing.T) {
	image := os.Getenv("DISPATCH_INSTALLATION_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_INSTALLATION_DOCKER_IMAGE to a local controller image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := fmt.Sprint(time.Now().UnixNano())
	makeInstallation := func(label string) (*Manager, Config) {
		m := New(t.TempDir())
		os.Chmod(m.Dir, 0700)
		m.Out = io.Discard
		m.Timeout = 90 * time.Second
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		name := "portable-" + label + "-" + suffix
		c := Config{Name: name, Volume: name + "-data", Image: image, Socket: "/var/run/docker.sock", Bind: "127.0.0.1", Port: port}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			m.compose(cleanup, c, "down", "--remove-orphans")
			if installed, err := m.Load(); err == nil && m.verifyVolume(cleanup, installed) == nil {
				m.Run(cleanup, "docker", "volume", "rm", c.Volume)
			}
		})
		return m, c
	}
	source, sc := makeInstallation("source")
	if err := source.Locked(func() error { return source.Install(ctx, sc, "", false) }); err != nil {
		t.Fatal(err)
	}
	username, password := "portable-owner", "disposable-portability-password"
	request := func(c Config, method, route, body string, status int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://127.0.0.1:%d/api/v1%s", c.Port, route), strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.SetBasicAuth(username, password)
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s returned %d", method, route, response.StatusCode)
		}
		return raw
	}
	request(sc, "POST", "/auth/setup", `{"username":"`+username+`","password":"`+password+`"}`, 201)
	created := request(sc, "POST", "/projects", `{"name":"Portable Docker project"}`, 201)
	request(sc, "POST", "/secrets", `{"name":"Portable credential","type":"text","environmentVariable":"PORTABLE_TOKEN","value":"disposable encrypted value"}`, 201)
	var project struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created, &project); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "dispatch.tar.gz")
	if err := source.Locked(func() error { return source.Export(ctx, archive) }); err != nil {
		t.Fatal(err)
	}
	request(sc, "GET", "/projects", "", 200) // Export must restart the source.
	if _, err := source.compose(ctx, sc, "stop", "dispatch"); err != nil {
		t.Fatal(err)
	}
	target, tc := makeInstallation("target")
	tc.Image = ""
	tc.PublicURL = "https://portable.example.com"
	if err := target.Locked(func() error { return target.Import(ctx, archive, tc, false) }); err != nil {
		t.Fatal(err)
	}
	request(tc, "POST", "/auth/login", `{"username":"`+username+`","password":"`+password+`"}`, 200)
	projects := request(tc, "GET", "/projects", "", 200)
	if !strings.Contains(string(projects), project.ID) {
		t.Fatal("project identity lost")
	}
	secrets := request(tc, "GET", "/secrets", "", 200)
	if !strings.Contains(string(secrets), "Portable credential") {
		t.Fatal("credential record lost")
	}
	// A second export checks the actual restored database/key pair, not just the
	// original archive, and confirms the imported controller remains portable.
	second := filepath.Join(t.TempDir(), "roundtrip.tar.gz")
	if err := target.Locked(func() error { return target.Export(ctx, second) }); err != nil {
		t.Fatal(err)
	}
	request(tc, "GET", "/projects", "", 200)
}

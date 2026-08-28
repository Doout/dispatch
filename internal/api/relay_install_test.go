package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

func TestRelayInstallerAssetsArePublic(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DISPATCH_RELAY_BINARY_ROOT", root)
	want := []byte("relay-binary")
	if err := os.WriteFile(filepath.Join(root, "linux-amd64"), want, 0o755); err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/relay/install.sh", nil))
	installer := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(installer, "dispatch-relay.service") {
		t.Fatalf("unexpected installer response: %d %s", response.Code, response.Body.String())
	}
	for _, marker := range []string{"DISPATCH_RELAY_INSTALL_MODE", "docker compose", "compose.yaml", "Re-run this command to update it"} {
		if !strings.Contains(installer, marker) {
			t.Fatalf("installer is missing Docker support marker %q", marker)
		}
	}
	check := exec.Command("sh", "-n")
	check.Stdin = strings.NewReader(installer)
	if output, err := check.CombinedOutput(); err != nil {
		t.Fatalf("installer shell syntax is invalid: %v: %s", err, output)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/relay/bin/linux-amd64", nil))
	if response.Code != http.StatusOK || response.Body.String() != string(want) {
		t.Fatalf("unexpected binary response: %d %q", response.Code, response.Body.String())
	}
}

func TestRelaySSHHelpers(t *testing.T) {
	for machine, want := range map[string]string{"x86_64": "linux-amd64", "amd64": "linux-amd64", "aarch64": "linux-arm64", "arm64": "linux-arm64"} {
		got, err := relayPlatform(machine)
		if err != nil || got != want {
			t.Fatalf("relayPlatform(%q) = %q, %v", machine, got, err)
		}
	}
	if _, err := relayPlatform("riscv64"); err == nil {
		t.Fatal("expected unsupported architecture error")
	}
	if got, detail := relaySSHAddress("relay.example.com", 0); detail != "" || got != "relay.example.com:22" {
		t.Fatalf("unexpected SSH address: %q %q", got, detail)
	}
	if _, detail := relaySSHAddress("relay.example.com; reboot", 22); detail == "" {
		t.Fatal("expected unsafe hostname to be rejected")
	}
	if got := shellQuote("it's-safe"); got != `'it'"'"'s-safe'` {
		t.Fatalf("unexpected shell quote: %q", got)
	}
	mode, image, detail := relayInstallOptions("docker", "registry.example.com/dispatch-relay:v2")
	if detail != "" || mode != "docker" || image != "registry.example.com/dispatch-relay:v2" {
		t.Fatalf("unexpected Docker install options: %q %q %q", mode, image, detail)
	}
	if _, _, detail := relayInstallOptions("systemd", "registry.example.com/relay:v2"); detail == "" {
		t.Fatal("expected a container image with systemd to be rejected")
	}
	command, stdin := relaySSHInstallerInvocation(relaySSHInstallRequest{
		User: "operator", SudoPassword: "sudo-secret", RelayToken: "0123456789abcdefghijklmn",
		InstallMode: "docker", RelayImage: "registry.example.com/dispatch-relay:v2",
	}, "https://relay.example.com", "/tmp/dispatch-relay")
	for _, marker := range []string{"DISPATCH_RELAY_INSTALL_MODE='docker'", "DISPATCH_RELAY_IMAGE='registry.example.com/dispatch-relay:v2'", "sudo -S"} {
		if !strings.Contains(command, marker) {
			t.Fatalf("SSH Docker invocation is missing %q: %s", marker, command)
		}
	}
	if !strings.HasPrefix(stdin, "sudo-secret\n") {
		t.Fatal("SSH Docker invocation did not pass the sudo password to the installer")
	}
}

func TestRelaySSHInstallRequiresAuthentication(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/relay/ssh/scan", strings.NewReader(`{"host":"localhost"}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
}

func TestRelaySSHInstallAcceptsSavedSSHKey(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()

	privateKey, _, err := generateSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	createBody, _ := json.Marshal(map[string]any{"name": "Relay key", "type": "ssh_private_key", "environmentVariable": "SSH_PRIVATE_KEY", "value": privateKey})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewReader(createBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create SSH key: %d %s", response.Code, response.Body.String())
	}
	var secret core.Secret
	if err := json.NewDecoder(response.Body).Decode(&secret); err != nil {
		t.Fatal(err)
	}

	installBody, _ := json.Marshal(map[string]any{
		"host": "127.0.0.1", "port": 1, "user": "root", "authType": "private_key", "secretId": secret.ID,
		"hostKeyFingerprint": "SHA256:test", "relayUrl": "https://relay.example.com", "relayToken": "0123456789abcdefghijklmn",
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/relay/ssh/install", bytes.NewReader(installBody)))
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "SSH connection failed") {
		t.Fatalf("saved SSH key was not accepted: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "PRIVATE KEY") {
		t.Fatalf("saved private key leaked in response: %s", response.Body.String())
	}
}

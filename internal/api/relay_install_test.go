package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "dispatch-relay.service") {
		t.Fatalf("unexpected installer response: %d %s", response.Code, response.Body.String())
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

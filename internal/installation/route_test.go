package installation

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestConfigureExistingInstallationRoute(t *testing.T) {
	m, original, calls := fixture(t)
	ctx := context.Background()
	configure := func() error { return m.ConfigureRoute(ctx, "dispatch.example.test", "proxy", "test-resolver", false) }
	if err := configure(); err != nil {
		t.Fatal(err)
	}
	current, err := m.Load()
	if err != nil || current.Hostname != "dispatch.example.test" || current.ImageID != original.ImageID || current.Volume != original.Volume || current.Bind != original.Bind || current.PublicURL != original.PublicURL {
		t.Fatalf("unexpected installation after route change: %+v, %v", current, err)
	}
	compose, err := os.ReadFile(m.path("compose.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Host(`dispatch.example.test`)", `"traefik.docker.network": "proxy"`, `.tls.certresolver": "test-resolver"`, `.loadbalancer.server.port": "8080"`} {
		if !strings.Contains(string(compose), label) {
			t.Errorf("missing %s", label)
		}
	}
	*calls = nil
	if err := configure(); err != nil || has(*calls, " up ") {
		t.Fatalf("repeated route restarted controller: %v, %v", *calls, err)
	}
	if err := m.ConfigureRoute(ctx, "", "", "", true); err != nil {
		t.Fatal(err)
	}
	current, err = m.Load()
	if err != nil || current.Hostname != "" || current.ProxyNetwork != "" || current.TLSResolver != "" {
		t.Fatal("route was not cleared", err)
	}
}
func TestConfigureRouteRestoresWorkingInstallation(t *testing.T) {
	m, original, _ := fixture(t)
	m.Health = func(_ context.Context, c Config) error {
		if c.Hostname != "" {
			return errors.New("unhealthy")
		}
		return nil
	}
	if err := m.ConfigureRoute(context.Background(), "dispatch.example.test", "proxy", "test-resolver", false); err == nil {
		t.Fatal("expected a failed health check")
	}
	if err := m.checkManaged(context.Background(), original); err != nil {
		t.Fatal("previous compose and controller were not restored", err)
	}
	current, err := m.Load()
	if err != nil || current.Hostname != "" {
		t.Fatal("failed route was persisted", err)
	}
}

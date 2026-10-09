package hostedruntime

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/tenancy"
	"github.com/oklog/ulid/v2"
)

func TestHostedRuntimeStartsWithRemoteGuardsAndIsolatedKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	factory, err := tenancy.NewFactory(tenancy.FactoryConfig{RootDir: filepath.Join(t.TempDir(), "tenants")})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	id := ulid.Make().String()
	runtime, err := factory.Open(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	auth := &api.HostedAuth{TenantID: id, LoginURL: "https://dispatch.example.test", Authenticate: func(*http.Request) (core.Identity, error) {
		return core.Identity{ID: "owner", Kind: "user", SystemRole: core.UserRoleOwner}, nil
	}}
	instance, err := New(ctx, runtime, tenancy.Tenant{ID: id}, auth, "https://team.dispatch.example.test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	for _, path := range []string{"/api/v1/operations/backups", "/api/v1/relay/ssh/install", "/api/v1/builders/ssh/scan", "/api/v1/private-networks/one/install-connector", "/api/v1/servers/one/repair", "/api/v1/helm/inspect", "/api/v1/deployments/one/resources/pod/one", "/api/v1/infrastructure/bootstrap/review"} {
		response := httptest.NewRecorder()
		instance.Handler.ServeHTTP(response, httptest.NewRequest("POST", path, nil))
		if response.Code != http.StatusConflict {
			t.Errorf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	instance.Handler.ServeHTTP(response, httptest.NewRequest("GET", "/api/v1/edge/nodes/one/workflow/jobs/next", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("worker route was not authenticated: %d %s", response.Code, response.Body.String())
	}
	info, err := os.Stat(runtime.Paths.VaultPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private tenant vault missing", err)
	}
	finished := make(chan struct{})
	go func() { instance.Close(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("tenant workers did not stop")
	}
}

func TestTenantVaultRejectsSymlinkAndPermissiveKey(t *testing.T) {
	key := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(key, []byte("not-a-key"), 0644); err != nil {
		t.Fatal(err)
	}
	if ensureKey(key) == nil {
		t.Fatal("permissive key accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(key, link); err != nil {
		t.Fatal(err)
	}
	if ensureKey(link) == nil {
		t.Fatal("symlink key accepted")
	}
}

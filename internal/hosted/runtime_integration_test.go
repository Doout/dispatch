package hosted_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/hosted"
	"github.com/doout/dispatch/internal/hostedruntime"
	"github.com/doout/dispatch/internal/tenancy"
)

// Exercise the real operational router and its background services: a fake
// tenant handler cannot catch a fallback to the legacy authentication system.
func TestOperationalTenantIsolation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	catalog, err := tenancy.Open(ctx, filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	if err = catalog.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range []tenancy.User{
		{ID: "platform", Name: "Platform", Email: "platform@example.test", PlatformAdmin: true, EmailVerified: true},
		{ID: "owner-a", Name: "Owner A", Email: "owner-a@example.test", EmailVerified: true},
		{ID: "owner-b", Name: "Owner B", Email: "owner-b@example.test", EmailVerified: true},
	} {
		if err = catalog.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	factory, err := tenancy.NewFactory(tenancy.FactoryConfig{RootDir: filepath.Join(root, "tenants")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { factory.Close() })
	cfg := hosted.Config{RootDomain: "dispatch.example.test", DataDirectory: root, ConsoleAddresses: []string{"192.0.2.10"}, WorkloadGateway: "192.0.2.11", Nameservers: []string{"ns1.example.net", "ns2.example.net"}, DNSReadToken: strings.Repeat("d", 40)}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := hosted.New(ctx, cfg, catalog, func(ctx context.Context, tenant tenancy.Tenant, auth *api.HostedAuth) (*hosted.TenantRuntime, error) {
		runtime, err := factory.Open(ctx, tenant.ID)
		if err != nil {
			return nil, err
		}
		return hostedruntime.New(ctx, runtime, tenant, auth, cfg.TenantOrigin(tenant.Slug), logger)
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	tenants := make([]tenancy.Tenant, 0, 2)
	tokens := make([]string, 0, 2)
	for _, name := range []string{"a", "b"} {
		tenant, err := catalog.CreateTenant(ctx, tenancy.CreateTenantInput{CreatorID: "platform", InitialOwnerID: "owner-" + name, Slug: "team-" + name, Name: "Team " + name})
		if err != nil {
			t.Fatal(err)
		}
		if err = catalog.SetTenantState(ctx, tenant.ID, tenancy.StateActive); err != nil {
			t.Fatal(err)
		}
		token, err := catalog.CreateSession(ctx, "owner-"+name, tenancy.TenantAudience(tenant.ID), time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		tenants, tokens = append(tenants, tenant), append(tokens, token)
	}
	request := func(host, method, path, token string, body any, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(method, "https://"+host+path, bytes.NewReader(payload))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	hostA, hostB := "team-a.dispatch.example.test", "team-b.dispatch.example.test"
	w := request(hostA, http.MethodPost, "/api/v1/projects", tokens[0], map[string]string{"name": "Private A", "description": "tenant-a-private-description"}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("create A project: %d %s", w.Code, w.Body.String())
	}
	var project core.Project
	if err = json.Unmarshal(w.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	w = request(hostB, http.MethodGet, "/api/v1/projects", tokens[1], nil, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), project.ID) || strings.Contains(w.Body.String(), "tenant-a-private") {
		t.Fatalf("B project list: %d %s", w.Code, w.Body.String())
	}
	w = request(hostB, http.MethodPut, "/api/v1/projects/"+project.ID, tokens[1], map[string]string{"name": "Changed by B"}, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("B changed A project: %d %s", w.Code, w.Body.String())
	}
	w = request(hostB, http.MethodPost, "/api/v1/projects", tokens[1], map[string]string{"name": "Private A"}, nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("same project name in B: %d %s", w.Code, w.Body.String())
	}
	w = request(hostA, http.MethodPost, "/api/v1/secrets", tokens[0], map[string]string{"name": "Private token", "type": "text", "environmentVariable": "PRIVATE_TOKEN", "value": "tenant-a-sensitive-value"}, nil)
	if w.Code != http.StatusCreated || strings.Contains(w.Body.String(), "tenant-a-sensitive-value") {
		t.Fatalf("create A secret: %d %s", w.Code, w.Body.String())
	}
	w = request(hostB, http.MethodGet, "/api/v1/secrets", tokens[1], nil, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "Private token") {
		t.Fatalf("B secret list: %d %s", w.Code, w.Body.String())
	}
	platformToken, err := catalog.CreateSession(ctx, "platform", tenancy.AudiencePlatform, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{platformToken, tokens[1]} {
		w = request(hostA, http.MethodGet, "/api/v1/projects", token, nil, nil)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("cross audience projects: %d %s", w.Code, w.Body.String())
		}
	}
	w = request(cfg.RootDomain, http.MethodGet, "/api/v1/secrets", platformToken, nil, nil)
	if w.Code != http.StatusNotFound {
		t.Fatalf("platform operational API: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/auth/login", "/api/v1/auth/setup", "/api/v1/users"} {
		w = request(hostA, http.MethodPost, path, tokens[0], map[string]string{"email": "intruder@example.test", "password": "private-password"}, nil)
		if w.Code != http.StatusForbidden {
			t.Fatalf("legacy identity %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = request(hostA, http.MethodGet, "/api/v1/projects", tokens[0], nil, map[string]string{"Impersonate-User": "owner-b"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("impersonation allowed: %d %s", w.Code, w.Body.String())
	}
	w = request(hostA, http.MethodGet, "/api/v1/projects", "", nil, map[string]string{"Authorization": "Basic b3duZXItYTpwYXNzd29yZA=="})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy basic auth allowed: %d %s", w.Code, w.Body.String())
	}
	var keys [][]byte
	for _, tenant := range tenants {
		runtime, err := factory.Open(ctx, tenant.ID)
		if err != nil {
			t.Fatal(err)
		}
		key, err := os.ReadFile(runtime.Paths.VaultPath)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if bytes.Equal(keys[0], keys[1]) {
		t.Fatal("tenant vault keys are shared")
	}
}

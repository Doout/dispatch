package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provider/mock"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func managerFixture(t *testing.T) (*Manager, string) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "providers.db")
	data, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	return &Manager{Store: data, Secrets: secretvalue.New(data, vault)}, path
}
func secretFixture(t *testing.T, m *Manager, id, value string) {
	t.Helper()
	resolver := m.Secrets.(*secretvalue.Resolver)
	cipher, err := resolver.Vault.Encrypt("secret:"+id, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := core.Secret{ID: id, Name: id, Type: core.SecretTypeAPIToken, Source: core.SecretSourceLocal, EnvironmentVariable: "PROVIDER_TOKEN", EncryptedValue: cipher, CreatedAt: now, UpdatedAt: now}
	data := m.Store.(*store.SQLStore)
	if _, err = data.GetSecret(context.Background(), id); err == nil {
		err = data.UpdateSecret(context.Background(), item)
	} else {
		err = data.CreateSecret(context.Background(), item)
	}
	if err != nil {
		t.Fatal(err)
	}
}
func TestRegistrationRestartRotationDisableAndIdentity(t *testing.T) {
	m, path := managerFixture(t)
	ctx := context.Background()
	secretFixture(t, m, "credential", "first-private-token")
	adapter, _ := mock.New(mock.Options{})
	var expected atomic.Value
	expected.Store("first-private-token")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.Handler(adapter, expected.Load().(string)).ServeHTTP(w, r)
	}))
	defer upstream.Close()
	caps := []string{provider.CapabilityInspect, provider.CapabilityCreate, provider.CapabilityDelete}
	item, err := m.Register(ctx, Registration{Name: "Mock", Endpoint: upstream.URL, CredentialSecretID: "credential", Enabled: true, Capabilities: caps})
	if err != nil || item.State != "ready" {
		t.Fatalf("register: %#v %v", item, err)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "private-token") {
		t.Fatal("credential escaped registration")
	}
	reopened, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.GetInfrastructureProvider(ctx, item.ID)
	if err != nil || saved.ManifestDigest != item.ManifestDigest || saved.CredentialSecretID != "credential" {
		t.Fatalf("restart lost registration: %#v %v", saved, err)
	}
	secretFixture(t, m, "credential", "next-private-token")
	expected.Store("next-private-token")
	item, err = m.Verify(ctx, item.ID, item.Revision)
	if err != nil || item.State != "ready" {
		t.Fatalf("rotation: %#v %v", item, err)
	}
	if _, _, err = m.Adapter(ctx, item.ID, provider.CapabilityCreate, item.ManifestDigest); err != nil {
		t.Fatal(err)
	}
	in := Registration{Name: item.Name, Endpoint: item.Endpoint, CredentialSecretID: item.CredentialSecretID, Capabilities: item.Capabilities, Revision: item.Revision, Enabled: false}
	item, err = m.Update(ctx, item.ID, in)
	if err != nil || item.State != "disabled" {
		t.Fatal(err)
	}
	if _, _, err = m.Adapter(ctx, item.ID, provider.CapabilityCreate, item.ManifestDigest); !errors.Is(err, ErrDisabled) {
		t.Fatal("disabled provider authorized mutation")
	}
	if _, _, err = m.Adapter(ctx, item.ID, provider.CapabilityInspect, item.ManifestDigest); err != nil {
		t.Fatal("disable erased inspectable connection", err)
	}
	in.Endpoint = "https://different.example"
	in.Revision = item.Revision
	if _, err = m.Update(ctx, item.ID, in); err == nil {
		t.Fatal("provider endpoint identity replaced")
	}
}
func TestRegistrationRejectsIncompatibleSensitiveOrInvalidManifest(t *testing.T) {
	for _, kind := range []string{"version", "schema", "credential-echo", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := managerFixture(t)
			secretFixture(t, m, "credential", "provider-private-token")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				manifest := provider.Manifest{APIVersion: provider.APIVersion, Name: "mock", DisplayName: "Mock", Version: "1", Capabilities: []string{provider.CapabilityInspect}, ConfigurationSchema: json.RawMessage(`{"type":"object","properties":{}}`)}
				switch kind {
				case "version":
					manifest.APIVersion = "unsupported"
				case "schema":
					manifest.ConfigurationSchema = json.RawMessage(`{"type":"object","properties":{"field":{"type":"not-a-json-type"}}}`)
				case "credential-echo":
					manifest.DisplayName = "provider-private-token"
				case "unavailable":
					w.WriteHeader(503)
					_, _ = w.Write([]byte("provider-private-token"))
					return
				}
				_ = json.NewEncoder(w).Encode(manifest)
			}))
			defer upstream.Close()
			item, err := m.Register(context.Background(), Registration{Name: "Rejected", Endpoint: upstream.URL, CredentialSecretID: "credential", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if item.Enabled || item.State != "failed" || strings.Contains(item.LastError, "private-token") || strings.Contains(string(item.Manifest), "private-token") {
				t.Fatalf("unsafe invalid registration: %#v", item)
			}
		})
	}
}
func TestConfigurationSchemaRejectsUnknownFieldsAndExternalReferences(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"count":{"type":"integer","minimum":1},"name":{"type":"string","maxLength":4}},"required":["count"]}`)
	for _, value := range []map[string]any{{"count": float64(2), "unknown": "private"}, {"count": "private"}, {"count": float64(0)}, {"name": "long-value"}} {
		if err := ValidateConfiguration(raw, value); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatalf("invalid config: %#v %v", value, err)
		}
	}
	if err := ValidateConfiguration(raw, map[string]any{"count": float64(2)}); err != nil {
		t.Fatal(err)
	}
	raw = json.RawMessage(`{"type":"object","properties":{"secret":{"$ref":"file:///etc/passwd"}}}`)
	if _, _, err := configurationSchema(raw); err == nil {
		t.Fatal("schema loaded an external reference")
	}
}
func TestProviderEndpointEnforcesTLSAndCredentialSeparation(t *testing.T) {
	for _, endpoint := range []string{"http://provider.example", "https://user:password@provider.example", "https://provider.example?token=secret", "https://provider.example#fragment"} {
		if _, err := Endpoint(endpoint, ""); err == nil {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
	if _, err := Endpoint("http://127.0.0.1:8091", "node"); err == nil {
		t.Fatal("edge route accepted HTTP")
	}
	if _, err := Endpoint("http://127.0.0.1:8091", ""); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateProviderUsesOnlyItsConfiguredEdgeRoute(t *testing.T) {
	m, _ := managerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := m.Store.(*store.SQLStore)
	resolver := m.Secrets.(*secretvalue.Resolver)
	m.Edge = edge.New(data, resolver.Vault)
	now := time.Now().UTC()
	if err := data.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "provider-node", Name: "Provider node", Driver: edge.DriverAgent, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	adapter, _ := mock.New(mock.Options{})
	upstream := httptest.NewTLSServer(provider.Handler(adapter, ""))
	defer upstream.Close()
	m.HTTPClient = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("direct fallback must not be used")
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			job, err := m.Edge.Lease(ctx, "provider-node")
			if err != nil || job == nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Millisecond * 5):
				}
				continue
			}
			request, err := http.NewRequestWithContext(ctx, job.Request.Method, job.Request.URL, bytes.NewReader(job.Request.Body))
			if err != nil {
				return
			}
			request.Header = http.Header(job.Request.Headers)
			response, err := upstream.Client().Do(request)
			if err != nil {
				return
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			_ = m.Edge.Complete(ctx, "provider-node", job.ID, edge.Completion{LeaseToken: job.LeaseToken, Response: &edge.HTTPResponse{StatusCode: response.StatusCode, Headers: response.Header, Body: body}})
		}
	}()
	defer func() { cancel(); <-done }()
	item, err := m.Register(ctx, Registration{Name: "Private mock", Endpoint: upstream.URL, PrivateNetworkID: "provider-node", Enabled: true})
	if err != nil || item.State != "ready" {
		t.Fatalf("private route failed: %#v %v", item, err)
	}
	options, err := m.Options(ctx, item.ID, "regions", nil)
	if err != nil || len(options) != 1 {
		t.Fatalf("private discovery: %#v %v", options, err)
	}
}

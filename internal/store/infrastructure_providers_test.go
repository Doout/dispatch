package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestInfrastructureProviderPersistence(t *testing.T) {
	testInfrastructureProviders(t, filepath.Join(t.TempDir(), "providers.db"))
}
func TestInfrastructureProviderPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_PROVIDER_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_PROVIDER_POSTGRES_URL to a disposable database")
	}
	testInfrastructureProviders(t, dsn)
}
func testInfrastructureProviders(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	data, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"provider-credential-one", "provider-credential-two"} {
		if err = data.CreateSecret(ctx, core.Secret{ID: id, Name: id, EnvironmentVariable: strings.ReplaceAll(id, "-", "_"), EncryptedValue: "encrypted", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	p := core.InfrastructureProvider{ID: "provider-registration", Name: "Provider", Endpoint: "https://provider.example", CredentialSecretID: "provider-credential-one", Enabled: true, Capabilities: []string{"server.inspect"}, Manifest: json.RawMessage(`{"name":"provider"}`), ManifestDigest: "manifest-digest", State: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now, LastVerifiedAt: &now}
	if err = data.CreateInfrastructureProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	p.CredentialSecretID = "provider-credential-two"
	p.Revision++
	if err = data.UpdateInfrastructureProvider(ctx, p, 1); err != nil {
		t.Fatal(err)
	}
	if err = data.UpdateInfrastructureProvider(ctx, p, 1); !errors.Is(err, ErrProviderChanged) {
		t.Fatal("stale update accepted", err)
	}
	p.Enabled = false
	p.State = "disabled"
	p.Revision++
	if err = data.UpdateInfrastructureProvider(ctx, p, 2); err != nil {
		t.Fatal(err)
	}
	saved, err := data.GetInfrastructureProvider(ctx, p.ID)
	if err != nil || saved.Enabled || saved.State != "disabled" || saved.CredentialSecretID != p.CredentialSecretID || saved.ManifestDigest != p.ManifestDigest {
		t.Fatalf("roundtrip: %#v %v", saved, err)
	}
	if err = data.DeleteSecret(ctx, p.CredentialSecretID); err == nil {
		t.Fatal("referenced credential deleted")
	}
	items, err := data.ListInfrastructureProviders(ctx)
	if err != nil || len(items) != 1 {
		t.Fatalf("history lost: %#v %v", items, err)
	}
}

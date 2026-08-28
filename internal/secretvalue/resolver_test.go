package secretvalue

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

type memoryStore struct {
	secret  core.Secret
	store   core.SecretStore
	network core.PrivateNetwork
}

func (m memoryStore) GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error) {
	return m.network, nil
}

func (m memoryStore) GetSecret(context.Context, string) (core.Secret, error) { return m.secret, nil }
func (m memoryStore) GetSecretStore(context.Context, string) (core.SecretStore, error) {
	return m.store, nil
}

func TestResolverFetchesIBMCloudSecretAtRuntime(t *testing.T) {
	var serviceURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "iam-token", "expiration": time.Now().Add(time.Hour).Unix()})
		case "/api/v2/secrets/secret-id":
			if r.Header.Get("Authorization") != "Bearer iam-token" {
				t.Fatalf("missing IAM token")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"payload": "runtime-value"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	serviceURL = server.URL
	keyPath := filepath.Join(t.TempDir(), "master.key")
	key := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	credentials, _ := json.Marshal(map[string]string{"apiKey": "key"})
	encrypted, err := vault.Encrypt("secret-store:store-1", credentials)
	if err != nil {
		t.Fatal(err)
	}
	r := New(memoryStore{
		secret: core.Secret{ID: "ref-1", Source: core.SecretSourceExternal, ExternalStoreID: "store-1", ExternalSecretID: "secret-id"},
		store:  core.SecretStore{ID: "store-1", Provider: ProviderIBMCloudSecretsManager, Config: map[string]string{"serviceUrl": serviceURL, "iamUrl": server.URL}, EncryptedCredentials: encrypted},
	}, vault)
	value, err := r.Resolve(context.Background(), "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "runtime-value" {
		t.Fatalf("got %q", value)
	}
}

func TestSelectValueSupportsNestedFields(t *testing.T) {
	value, err := selectValue(map[string]any{"credentials": map[string]any{"password": "secret"}}, "credentials.password")
	if err != nil || value != "secret" {
		t.Fatalf("got %q, %v", value, err)
	}
}

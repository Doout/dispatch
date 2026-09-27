package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/secretvalue"
)

func TestJobResolvesJSONKeysFromSavedValues(t *testing.T) {
	ctx := context.Background()
	data, _, _ := workflowRunnerFixture(t, "json-bindings")
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	contents := `{"APIKEY":"private-key","connection":{"URL":"https://example.test"}}`
	encrypted, err := vault.Encrypt("secret:bundle", []byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, item := range []core.Secret{
		{ID: "bundle", Name: "APP_CONFIG", Type: core.SecretTypeJSON, Source: core.SecretSourceLocal, EnvironmentVariable: "APP_CONFIG", EncryptedValue: encrypted, CreatedAt: now, UpdatedAt: now},
		{ID: "plain", Name: "Public settings", Type: core.SecretTypeEnvironmentJSON, Source: core.SecretSourceLocal, EnvironmentVariable: "PUBLIC_SETTINGS", PublicValue: `{"URL":"https://plain.example.test"}`, CreatedAt: now, UpdatedAt: now},
	} {
		if err := data.CreateSecret(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	runtime := jobRuntime{service: &Service{Store: data, Secrets: secretvalue.New(data, vault)}}
	values, err := runtime.resolveSecrets(ctx, map[string]SecretBinding{
		"APIKEY":     {SecretRef: "APP_CONFIG", Key: "APIKEY"},
		"URL":        {SecretRef: "bundle", Key: "connection.URL"},
		"PUBLIC_URL": {SecretRef: "plain", Key: "URL"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if values["APIKEY"] != "private-key" || values["URL"] != "https://example.test" || values["PUBLIC_URL"] != "https://plain.example.test" {
		t.Fatal("JSON keys were not resolved into their target environment variables")
	}
	if strings.Contains(redactJobLog("token private-key", values), "private-key") {
		t.Fatal("selected JSON credential was not redacted from job logs")
	}
	_, err = runtime.resolveSecrets(ctx, map[string]SecretBinding{"MISSING": {SecretRef: "bundle", Key: "missing"}})
	if err == nil || !strings.Contains(err.Error(), "JSON key missing is not available") || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("missing key error: %v", err)
	}
	_, err = runtime.resolveSecrets(ctx, map[string]SecretBinding{"OBJECT": {SecretRef: "bundle", Key: "connection"}})
	if err == nil || !strings.Contains(err.Error(), "must contain a string") {
		t.Fatalf("object key error: %v", err)
	}
}

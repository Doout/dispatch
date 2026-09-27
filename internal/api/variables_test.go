package api

import (
	"bytes"
	"context"
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

func TestServiceRejectsPlainVariableAsCredential(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := a.store.CreateSecret(ctx, core.Secret{ID: "plain-url", Name: "URL", Type: core.SecretTypeEnvironment, Source: core.SecretSourceLocal, EnvironmentVariable: "DEV_URL", PublicValue: "https://example.test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/services", map[string]any{
		"projectId": projects[0].ID, "name": "plain-credential", "type": "generic",
		"fields": map[string]any{"token": map[string]any{"secretRef": "plain-url"}},
	}, http.StatusBadRequest)
	if err := a.store.CreateSecret(ctx, core.Secret{ID: "plain-json", Name: "Public bundle", Type: core.SecretTypeEnvironmentJSON, Source: core.SecretSourceLocal, EnvironmentVariable: "PUBLIC_BUNDLE", PublicValue: `{"URL":"https://example.test"}`, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/services", map[string]any{
		"projectId": projects[0].ID, "name": "plain-json-credential", "type": "generic",
		"fields": map[string]any{"token": map[string]any{"secretRef": "plain-json"}},
	}, http.StatusBadRequest)

	raw := serviceRequestTest(t, a, "POST", "/api/v1/secrets", map[string]any{
		"name": "Service token", "type": "text", "environmentVariable": "SERVICE_TOKEN", "value": "private-token",
	}, http.StatusCreated)
	var credential core.Secret
	if err := json.Unmarshal(raw, &credential); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateService(ctx, core.Service{ID: "bound-service", Name: "bound-service", ProjectID: projects[0].ID, Type: "generic", Fields: map[string]core.ServiceField{"token": {SecretRef: credential.ID, Sensitive: true, Configured: true}}, Revision: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/secrets/"+credential.ID, map[string]any{
		"name": "Service token", "type": "environment_variable", "environmentVariable": "SERVICE_TOKEN", "value": "visible",
	}, http.StatusConflict)
}

func TestPlainVariableCanBeSavedWithoutSecretStorage(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"Development URL","type":"environment_variable","environmentVariable":"DEV_URL","value":"https://example.test/app"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create plain variable: %d %s", response.Code, response.Body.String())
	}
	var saved core.Secret
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Type != core.SecretTypeEnvironment || saved.PublicValue != "https://example.test/app" || bytes.Contains(response.Body.Bytes(), []byte("encryptedValue")) {
		t.Fatalf("unexpected variable response: %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/secrets/"+saved.ID, bytes.NewBufferString(`{"name":"Development URL","type":"environment_variable","environmentVariable":"DEV_URL","value":"https://new.example.test/app"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("update plain variable: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.PublicValue != "https://new.example.test/app" {
		t.Fatalf("value was not updated: %#v", saved)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"External URL","type":"environment_variable","source":"external","environmentVariable":"EXTERNAL_URL","value":"https://example.test"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("plain external value should be rejected: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"Public settings","type":"environment_json","environmentVariable":"PUBLIC_SETTINGS","value":"{\"URL\":\"https://example.test\"}"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create plain JSON without vault: %d %s", response.Code, response.Body.String())
	}
}

func TestJSONVariablesRequireObjectsAndKeepSecretContentsPrivate(t *testing.T) {
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
	call := func(method, path, body string, want int) core.Secret {
		t.Helper()
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(method, path, bytes.NewBufferString(body)))
		if response.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, response.Code, response.Body.String())
		}
		if want >= 400 {
			return core.Secret{}
		}
		var item core.Secret
		if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
		return item
	}
	call(http.MethodPost, "/api/v1/secrets", `{"name":"bundle","type":"json","environmentVariable":"BUNDLE","value":"[]"}`, http.StatusBadRequest)
	call(http.MethodPost, "/api/v1/secrets", `{"name":"bundle","type":"environment_json","environmentVariable":"BUNDLE","value":"{broken"}`, http.StatusBadRequest)
	secret := call(http.MethodPost, "/api/v1/secrets", `{"name":"private bundle","type":"json","environmentVariable":"PRIVATE_BUNDLE","value":"{\"APIKEY\":\"private-key\",\"URL\":\"https://example.test\"}"}`, http.StatusCreated)
	if secret.PublicValue != "" {
		t.Fatalf("secret JSON appeared in API response: %#v", secret)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/secrets", nil))
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("private-key")) {
		t.Fatalf("secret JSON appeared in list response: %d", response.Code)
	}
	plain := call(http.MethodPost, "/api/v1/secrets", `{"name":"plain bundle","type":"environment_json","environmentVariable":"PLAIN_BUNDLE","value":"{\"URL\":\"https://example.test\"}"}`, http.StatusCreated)
	if plain.PublicValue != `{"URL":"https://example.test"}` {
		t.Fatalf("plain JSON missing: %#v", plain)
	}
	call(http.MethodPut, "/api/v1/secrets/"+plain.ID, `{"name":"plain bundle","type":"environment_json","environmentVariable":"PLAIN_BUNDLE","value":"not JSON"}`, http.StatusBadRequest)
}

func TestChangingPlainVariableToSecretNeedsNewValue(t *testing.T) {
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

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"Token","type":"environment_variable","environmentVariable":"TOKEN","value":"visible"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create plain variable: %d %s", response.Code, response.Body.String())
	}
	var saved core.Secret
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/secrets/"+saved.ID, bytes.NewBufferString(`{"name":"Token","type":"api_token","environmentVariable":"TOKEN"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("conversion without a new value should fail: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/secrets/"+saved.ID, bytes.NewBufferString(`{"name":"Token","type":"api_token","environmentVariable":"TOKEN","value":"private-token"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("convert to secret: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private-token")) || bytes.Contains(response.Body.Bytes(), []byte("visible")) {
		t.Fatalf("secret value leaked: %s", response.Body.String())
	}
	saved = core.Secret{}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Type != core.SecretTypeAPIToken || saved.PublicValue != "" {
		t.Fatalf("unexpected converted secret: %#v", saved)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/secrets/"+saved.ID, bytes.NewBufferString(`{"name":"Token URL","type":"environment_variable","environmentVariable":"TOKEN_URL","value":"https://example.test/token"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("convert to plain variable: %d %s", response.Code, response.Body.String())
	}
	saved = core.Secret{}
	if err := json.Unmarshal(response.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Type != core.SecretTypeEnvironment || saved.PublicValue != "https://example.test/token" {
		t.Fatalf("unexpected converted variable: %#v", saved)
	}
}

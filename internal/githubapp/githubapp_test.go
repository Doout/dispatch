package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
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
)

type testStore struct{ connection core.GitHubAppConnection }

func (s testStore) GetGitHubApp(context.Context, string) (core.GitHubAppConnection, error) {
	return s.connection, nil
}
func (s testStore) GetPrivateNetwork(context.Context, string) (core.PrivateNetwork, error) {
	return core.PrivateNetwork{}, errors.New("not configured")
}

func TestNormalizeEndpoints(t *testing.T) {
	tests := []struct{ web, api, wantWeb, wantAPI string }{
		{"", "", "https://github.com", "https://api.github.com"},
		{"https://github.example.com/", "", "https://github.example.com", "https://github.example.com/api/v3"},
		{"https://git.example.com", "https://api.git.example.com/v3/", "https://git.example.com", "https://api.git.example.com/v3"},
	}
	for _, test := range tests {
		web, api, err := NormalizeEndpoints(test.web, test.api)
		if err != nil || web != test.wantWeb || api != test.wantAPI {
			t.Fatalf("NormalizeEndpoints(%q, %q) = %q, %q, %v", test.web, test.api, web, api, err)
		}
	}
}

func TestParsePrivateKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	value := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if _, err := ParsePrivateKey(string(value)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePrivateKey("not a key"); err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("expected PEM validation error, got %v", err)
	}
}

func TestValidateRepositoryHost(t *testing.T) {
	if err := ValidateRepositoryHost("https://github.example.com/platform/charts.git", "https://github.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRepositoryHost("https://github.com/platform/charts.git", "https://github.example.com"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected host mismatch, got %v", err)
	}
}

func TestConvertManifestReturnsRegistrationOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/app-manifests/one-time-code/conversions" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"client_id":"Iv1.test","client_secret":"generated-secret","name":"Dispatch-a1b2c3d4","slug":"dispatch-a1b2c3d4","pem":"private-key","webhook_secret":"0123456789abcdef","owner":{"login":"platform","type":"Organization"}}`))
	}))
	defer server.Close()

	result, err := New(nil, nil).ConvertManifest(context.Background(), server.URL, "one-time-code")
	if err != nil {
		t.Fatal(err)
	}
	if result.AppID != 42 || result.ClientSecret != "generated-secret" || result.RegistrationOwner != "platform" || result.RegistrationOwnerType != "Organization" {
		t.Fatalf("unexpected manifest conversion: %#v", result)
	}
}

func TestConvertManifestAllowsPollingOnlyApp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"name":"Dispatch-polling","slug":"dispatch-polling","pem":"private-key"}`))
	}))
	defer server.Close()

	result, err := New(nil, nil).ConvertManifest(context.Background(), server.URL, "one-time-code")
	if err != nil {
		t.Fatal(err)
	}
	if result.AppID != 42 || result.PrivateKey != "private-key" || result.WebhookSecret != "" {
		t.Fatalf("unexpected polling-only manifest conversion: %#v", result)
	}
}

func TestInstallationTokenIsSignedAndCached(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/app/installations/73/access_tokens" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Errorf("expected signed App JWT, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"installation-token","expires_at":"` + time.Now().Add(time.Hour).UTC().Format(time.RFC3339) + `"}`))
	}))
	defer server.Close()
	manager := New(nil, vault)
	privateKey, webhookSecret, err := manager.EncryptCredentials("connection-1", string(pemValue), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	manager.Store = testStore{connection: core.GitHubAppConnection{ID: "connection-1", APIURL: server.URL, AppID: 42,
		InstallationID: 73, EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret}}
	for range 2 {
		token, err := manager.InstallationToken(context.Background(), "connection-1")
		if err != nil || token != "installation-token" {
			t.Fatalf("unexpected installation token %q, %v", token, err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("expected one token exchange, got %d", requests.Load())
	}
	manager.Invalidate("connection-1")
	if _, err := manager.InstallationToken(context.Background(), "connection-1"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("expected a new exchange after invalidation, got %d", requests.Load())
	}
}

func TestListRepositoriesUsesInstallationTokenAndPaginates(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC()})
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			if r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Errorf("unexpected repository authorization %q", r.Header.Get("Authorization"))
			}
			page := r.URL.Query().Get("page")
			repositories := []map[string]any{}
			if page == "1" {
				for index := 1; index <= 100; index++ {
					repositories = append(repositories, map[string]any{"id": index, "full_name": "platform/service", "name": "service", "default_branch": "main", "private": true, "html_url": "https://github.example.com/platform/service", "owner": map[string]string{"login": "platform"}})
				}
			} else if page == "2" {
				repositories = append(repositories, map[string]any{"id": 101, "full_name": "platform/ui", "name": "ui", "default_branch": "trunk", "private": false, "html_url": "https://github.example.com/platform/ui", "owner": map[string]string{"login": "platform"}})
			} else {
				t.Errorf("unexpected page %q", page)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "repositories": repositories})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := New(nil, vault)
	privateKey, webhookSecret, err := manager.EncryptCredentials("connection-1", string(pemValue), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	manager.Store = testStore{connection: core.GitHubAppConnection{ID: "connection-1", APIURL: server.URL, AppID: 42,
		InstallationID: 73, EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret}}
	repositories, err := manager.ListRepositories(context.Background(), "connection-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 101 || repositories[100].FullName != "platform/ui" || repositories[100].DefaultBranch != "trunk" {
		t.Fatalf("unexpected repositories: count=%d last=%#v", len(repositories), repositories[len(repositories)-1])
	}
}

func TestWorkflowRepositoryOperations(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	var webhookConfigured, statusPublished atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC()})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/platform/config/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "abc123"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/platform/config/git/trees/abc123":
			if r.URL.Query().Get("recursive") != "1" {
				t.Errorf("recursive tree query missing")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []map[string]any{{"path": ".dispatch/app.yaml", "type": "blob", "sha": "blob-1", "size": 42}, {"path": "README.md", "type": "blob", "sha": "blob-2", "size": 10}}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/platform/config/git/blobs/blob-1":
			contents := "apiVersion: dispatch/v1alpha1\nkind: Application\n"
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(contents)), "size": len(contents)})
		case r.Method == http.MethodPatch && r.URL.Path == "/app/hook/config":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["url"] != "https://dispatch.example/api/v1/events/github/apps/connection-1" || payload["secret"] != "0123456789abcdef" {
				t.Errorf("unexpected webhook payload: %#v", payload)
			}
			webhookConfigured.Store(true)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/platform/config/statuses/abc123":
			var payload map[string]string
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["state"] != "success" || payload["context"] != "Dispatch/deployment" {
				t.Errorf("unexpected status payload: %#v", payload)
			}
			statusPublished.Store(true)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := New(nil, vault)
	privateKey, webhookSecret, err := manager.EncryptCredentials("connection-1", string(pemValue), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	manager.Store = testStore{connection: core.GitHubAppConnection{ID: "connection-1", APIURL: server.URL, AppID: 42, InstallationID: 73,
		WebhookURL: "https://dispatch.example/api/v1/events/github/apps/connection-1", EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret}}
	head, err := manager.RepositoryHead(context.Background(), "connection-1", "platform/config", "main")
	if err != nil || head != "abc123" {
		t.Fatalf("unexpected repository head %q: %v", head, err)
	}
	files, err := manager.RepositoryFiles(context.Background(), "connection-1", "platform/config", head, ".dispatch")
	if err != nil || len(files) != 1 || files[0].Path != ".dispatch/app.yaml" {
		t.Fatalf("unexpected repository files: %#v err=%v", files, err)
	}
	if err := manager.EnsureWebhookConfig(context.Background(), "connection-1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetCommitStatus(context.Background(), "connection-1", "platform/config", head, "success", "Deployment succeeded", ""); err != nil {
		t.Fatal(err)
	}
	if !webhookConfigured.Load() || !statusPublished.Load() {
		t.Fatal("workflow repository operations were not sent")
	}
}

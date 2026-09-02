package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/githubapp"
)

func TestAuthProviderManifestCreatesLoginApp(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	github := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/app-manifests/one-time-code/conversions") {
			t.Fatalf("unexpected manifest conversion request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42,"client_id":"Iv1.login","client_secret":"generated-login-secret","pem":"unused-private-key"}`))
	}))
	defer github.Close()
	manager := githubapp.New(nil, vault)
	manager.Client = github.Client()
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret", PublicURL: "https://dispatch.example.com"}, false, EventConfig{Vault: vault, GitHubApps: manager})
	defer cleanup()

	body := `{"name":"Company login","type":"github","baseUrl":"` + github.URL + `","apiUrl":"` + github.URL + `","ownerType":"organization","owner":"platform","provisioning":"existing","state":"ready"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/auth/providers/manifest", bytes.NewBufferString(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("start provider manifest: %d %s", response.Code, response.Body.String())
	}
	var flow struct {
		Action   string                 `json:"action"`
		Manifest map[string]interface{} `json:"manifest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&flow); err != nil {
		t.Fatal(err)
	}
	manifestPermissions, _ := flow.Manifest["default_permissions"].(map[string]interface{})
	if flow.Manifest["public"] != true || manifestPermissions["emails"] != "read" {
		t.Fatalf("unexpected login manifest: %#v", flow.Manifest)
	}
	if name, _ := flow.Manifest["name"].(string); !strings.HasPrefix(name, "Dispatch-login-") {
		t.Fatalf("expected a unique GitHub App name, got %#v", flow.Manifest["name"])
	}
	action, err := url.Parse(flow.Action)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(action.Path, "/organizations/platform/settings/apps/new") {
		t.Fatalf("unexpected registration URL: %s", flow.Action)
	}

	response = httptest.NewRecorder()
	callback := "/api/v1/auth/providers/manifest/callback?state=" + url.QueryEscape(action.Query().Get("state")) + "&code=one-time-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, callback, nil))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "authProviderStatus=created") {
		t.Fatalf("complete provider manifest: %d %s %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/access", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"clientId":"Iv1.login"`)) || bytes.Contains(response.Body.Bytes(), []byte("generated-login-secret")) {
		t.Fatalf("saved provider response: %d %s", response.Code, response.Body.String())
	}
}

func TestAuthProviderCreationAndDiscovery(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret", PublicURL: "https://dispatch.example.com"}, false, EventConfig{Vault: vault})
	defer cleanup()

	body := `{"name":"Company GitHub","type":"github","baseUrl":"https://github.example.com","clientId":"client-123","clientSecret":"do-not-return","provisioning":"existing","state":"ready"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/auth/providers", bytes.NewBufferString(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create provider: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("do-not-return")) || bytes.Contains(response.Body.Bytes(), []byte("encryptedClientSecret")) {
		t.Fatalf("provider secret leaked: %s", response.Body.String())
	}
	var provider core.AuthProvider
	if err := json.NewDecoder(response.Body).Decode(&provider); err != nil {
		t.Fatal(err)
	}
	if provider.APIURL != "https://github.example.com/api/v3" || !provider.ClientSecretConfigured {
		t.Fatalf("unexpected provider: %#v", provider)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/auth/providers", bytes.NewBufferString(`{"name":"company github","type":"github","baseUrl":"https://github.example.com","clientId":"another-client","clientSecret":"another-secret"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected duplicate name conflict, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil))
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte("client-123")) {
		t.Fatalf("unsafe public provider response: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/discover", bytes.NewBufferString(`{"identifier":"user@example.com"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("discover provider: %d %s", response.Code, response.Body.String())
	}
	var discovery authDiscovery
	if err := json.NewDecoder(response.Body).Decode(&discovery); err != nil {
		t.Fatal(err)
	}
	if discovery.Method != "choose" || len(discovery.Providers) != 1 || discovery.Providers[0].ID != provider.ID {
		t.Fatalf("unexpected discovery: %#v", discovery)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers/"+provider.ID+"/start", bytes.NewBufferString(`{}`)))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("redirect_uri=https%3A%2F%2Fdispatch.example.com%2Fapi%2Fv1%2Fauth%2Fcallback")) {
		t.Fatalf("start OAuth: %d %s", response.Code, response.Body.String())
	}
}

func TestAuthProviderApprovalEnrollmentNeedsNoEmailRule(t *testing.T) {
	provider, _, err := normalizeAuthProvider(authProviderRequest{
		Name:         "Company GitHub",
		Type:         core.AuthProviderGitHub,
		BaseURL:      "https://github.example.com",
		ClientID:     "client-id",
		Provisioning: core.AuthProvisionApproval,
	}, nil, true)
	if err != nil || provider.Provisioning != core.AuthProvisionApproval {
		t.Fatalf("expected approval enrollment, got %#v %v", provider, err)
	}
}

func TestGitHubProviderDoesNotDisableLocalPasswords(t *testing.T) {
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

	for _, user := range []string{
		`{"username":"member","displayName":"Member","email":"member@example.com","password":"member-password","systemRole":"member","state":"active"}`,
		`{"username":"owner","displayName":"Owner","email":"owner@example.com","password":"owner-password","systemRole":"owner","state":"active"}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(user)))
		if response.Code != http.StatusCreated {
			t.Fatalf("create user: %d %s", response.Code, response.Body.String())
		}
	}

	provider := `{"name":"Company GitHub","type":"github","baseUrl":"https://github.example.com","clientId":"client-123","clientSecret":"secret","provisioning":"existing","state":"ready"}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/auth/providers", bytes.NewBufferString(provider)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create provider: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"member@example.com","password":"member-password"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected member password login, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"owner@example.com","password":"owner-password"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected owner recovery login, got %d: %s", response.Code, response.Body.String())
	}
}

func TestAuthProviderRequiresOwnerAndSecretStorage(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{})
	defer cleanup()
	body := bytes.NewBufferString(`{"name":"GitHub","type":"github","baseUrl":"https://github.com","clientId":"client","clientSecret":"secret"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/providers", body))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected authentication, got %d", response.Code)
	}
	body = bytes.NewBufferString(`{"name":"GitHub","type":"github","baseUrl":"https://github.com","clientId":"client","clientSecret":"secret"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/auth/providers", body))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected secret storage requirement, got %d: %s", response.Code, response.Body.String())
	}
}

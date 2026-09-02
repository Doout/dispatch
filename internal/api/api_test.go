package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/githubapp"
	relayservice "github.com/doout/dispatch/internal/relay"
	"github.com/doout/dispatch/internal/store"
)

func TestOverviewRequiresConfiguredToken(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if !overview.Demo || len(overview.Apps) != 3 {
		t.Fatalf("unexpected overview: demo=%v apps=%d", overview.Demo, len(overview.Apps))
	}
}

func TestProjectRoleFiltersOverviewAndBlocksControllerWrites(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var ownerOverview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&ownerOverview); err != nil {
		t.Fatal(err)
	}
	if len(ownerOverview.Projects) < 1 {
		t.Fatalf("expected demo projects, got %d", len(ownerOverview.Projects))
	}
	projectID := ownerOverview.Projects[0].ID

	userBody := bytes.NewBufferString(`{"username":"release-viewer","displayName":"Release Viewer","password":"a-valid-password-123","systemRole":"member","state":"active"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/users", userBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", response.Code, response.Body.String())
	}
	var user core.User
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		t.Fatal(err)
	}
	grantBody, _ := json.Marshal(map[string]string{"principalType": "user", "principalId": user.ID, "projectId": projectID, "role": "viewer"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/role-assignments", bytes.NewReader(grantBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("grant project access: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"release-viewer","password":"a-valid-password-123"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	var login map[string]string
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	memberRequest := func(method, target string, body io.Reader) *http.Request {
		request := httptest.NewRequest(method, target, body)
		request.Header.Set("Authorization", "Bearer "+login["token"])
		return request
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, memberRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("member overview: %d %s", response.Code, response.Body.String())
	}
	var memberOverview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&memberOverview); err != nil {
		t.Fatal(err)
	}
	if len(memberOverview.Projects) != 1 || memberOverview.Projects[0].ID != projectID {
		t.Fatalf("unexpected project visibility: %#v", memberOverview.Projects)
	}
	permissions := memberOverview.ProjectPermissions[projectID]
	if len(permissions) != 1 || permissions[0] != core.PermissionProjectView {
		t.Fatalf("unexpected viewer permissions: %#v", permissions)
	}
	if len(memberOverview.Secrets) != 0 || len(memberOverview.GitHubApps) != 0 {
		t.Fatal("controller credentials were visible to a project member")
	}
	if memberOverview.Secrets == nil || memberOverview.SecretStores == nil || memberOverview.PrivateNetworks == nil || memberOverview.GitHubApps == nil || memberOverview.RelayWebhooks == nil {
		t.Fatal("restricted controller collections must be encoded as empty arrays")
	}
	appID := ""
	for _, app := range ownerOverview.Apps {
		if app.ProjectID == projectID {
			appID = app.ID
			break
		}
	}
	if appID == "" {
		t.Fatal("expected an application in the visible project")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, memberRequest(http.MethodPost, "/api/v1/apps/"+appID+"/deployments", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected viewer deployment to be forbidden, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, memberRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"name":"Blocked"}`)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected project creation to be forbidden, got %d: %s", response.Code, response.Body.String())
	}

	if len(ownerOverview.Servers) == 0 {
		t.Fatal("expected a server in the owner overview")
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, memberRequest(http.MethodPut, "/api/v1/servers/"+ownerOverview.Servers[0].ID, bytes.NewBufferString(`{"name":"blocked"}`)))
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected server editing to be forbidden, got %d: %s", response.Code, response.Body.String())
	}

	disableBody := bytes.NewBufferString(`{"state":"disabled"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/users/"+user.ID, disableBody))
	if response.Code != http.StatusOK {
		t.Fatalf("disable user: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, memberRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected a disabled user's session to stop immediately, got %d", response.Code)
	}
}

func TestControllerOwnerCanImpersonateActiveUser(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	userBody := bytes.NewBufferString(`{"username":"permission-tester","displayName":"Permission Tester","password":"a-valid-password-123","systemRole":"member","state":"active"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/users", userBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", response.Code, response.Body.String())
	}
	var user core.User
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		t.Fatal(err)
	}

	request := tokenRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set(impersonateUserHeader, user.ID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("impersonated overview: %d %s", response.Code, response.Body.String())
	}
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if overview.Identity.ID != user.ID || overview.Identity.SystemRole != core.UserRoleMember {
		t.Fatalf("expected member identity, got %#v", overview.Identity)
	}
	if overview.Impersonator == nil || overview.Impersonator.ID != "controller-owner" {
		t.Fatalf("expected controller owner as impersonator, got %#v", overview.Impersonator)
	}
	if len(overview.Projects) != 0 {
		t.Fatalf("expected target user's project visibility, got %d projects", len(overview.Projects))
	}

	request = tokenRequest(http.MethodGet, "/api/v1/access", nil)
	request.Header.Set(impersonateUserHeader, user.ID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected target user's access policy, got %d: %s", response.Code, response.Body.String())
	}

	request = tokenRequest(http.MethodPut, "/api/v1/auth/password", nil)
	request.Header.Set(impersonateUserHeader, user.ID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected credential changes to be blocked, got %d: %s", response.Code, response.Body.String())
	}

	disableBody := bytes.NewBufferString(`{"state":"disabled"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/users/"+user.ID, disableBody))
	if response.Code != http.StatusOK {
		t.Fatalf("disable user: %d %s", response.Code, response.Body.String())
	}
	request = tokenRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set(impersonateUserHeader, user.ID)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("expected inactive user to be rejected, got %d: %s", response.Code, response.Body.String())
	}
}

func TestMemberCannotImpersonateAnotherUser(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	userBody := bytes.NewBufferString(`{"username":"regular-member","displayName":"Regular Member","password":"a-valid-password-123","systemRole":"member","state":"active"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/users", userBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"regular-member","password":"a-valid-password-123"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("login: %d %s", response.Code, response.Body.String())
	}
	var login map[string]string
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+login["token"])
	request.Header.Set(impersonateUserHeader, "another-user")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("expected impersonation to be forbidden, got %d: %s", response.Code, response.Body.String())
	}
}

func TestUsersCanOnlyChangeTheirOwnPassword(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(`{"username":"password-owner","displayName":"Password Owner","password":"original-password-123","systemRole":"member","state":"active"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", response.Code, response.Body.String())
	}
	var user core.User
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/users/"+user.ID, bytes.NewBufferString(`{"password":"owner-forced-password-123"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected owner password update to be rejected, got %d: %s", response.Code, response.Body.String())
	}

	login := func(password string) (int, string) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"password-owner","password":"`+password+`"}`)))
		var result map[string]string
		_ = json.NewDecoder(response.Body).Decode(&result)
		return response.Code, result["token"]
	}
	status, token := login("original-password-123")
	if status != http.StatusOK || token == "" {
		t.Fatalf("login with original password: %d", status)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/v1/auth/password", bytes.NewBufferString(`{"currentPassword":"wrong-password","newPassword":"replacement-password-123"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected the current password to be required, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPut, "/api/v1/auth/password", bytes.NewBufferString(`{"currentPassword":"original-password-123","newPassword":"replacement-password-123"}`))
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("change password: %d %s", response.Code, response.Body.String())
	}
	if status, _ := login("original-password-123"); status != http.StatusUnauthorized {
		t.Fatalf("expected old password to fail, got %d", status)
	}
	if status, _ := login("replacement-password-123"); status != http.StatusOK {
		t.Fatalf("expected new password to work, got %d", status)
	}
}

func TestTeamRoleGrantsProjectAccess(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var ownerOverview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&ownerOverview); err != nil {
		t.Fatal(err)
	}
	projectID := ownerOverview.Projects[0].ID

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/users", bytes.NewBufferString(`{"username":"team-viewer","displayName":"Team Viewer","password":"a-valid-password-123","systemRole":"member","state":"active"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", response.Code, response.Body.String())
	}
	var user core.User
	if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
		t.Fatal(err)
	}
	teamBody, _ := json.Marshal(map[string]any{"name": "Release", "memberIds": []string{user.ID}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/teams", bytes.NewReader(teamBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create team: %d %s", response.Code, response.Body.String())
	}
	var team core.Team
	if err := json.NewDecoder(response.Body).Decode(&team); err != nil {
		t.Fatal(err)
	}
	grantBody, _ := json.Marshal(map[string]string{"principalType": "team", "principalId": team.ID, "projectId": projectID, "role": "viewer"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/role-assignments", bytes.NewReader(grantBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("grant team access: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"team-viewer","password":"a-valid-password-123"}`)))
	var login map[string]string
	if err := json.NewDecoder(response.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+login["token"])
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("team member overview: %d %s", response.Code, response.Body.String())
	}
	var memberOverview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&memberOverview); err != nil {
		t.Fatal(err)
	}
	if len(memberOverview.Projects) != 1 || memberOverview.Projects[0].ID != projectID {
		t.Fatalf("team grant did not expose its project: %#v", memberOverview.Projects)
	}
}

func TestHealthDoesNotRequireToken(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestGitHubAppManifestIsPublic(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{})
	defer cleanup()
	body := bytes.NewBufferString(`{"name":"Dispatch-test","webUrl":"https://github.example.com","ownerType":"personal","eventDelivery":"none"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/github-apps/manifest", body))
	if response.Code != http.StatusOK {
		t.Fatalf("start GitHub App manifest: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Manifest map[string]any `json:"manifest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if public, ok := result.Manifest["public"].(bool); !ok || !public {
		t.Fatalf("GitHub App manifest must be public: %#v", result.Manifest)
	}
}

func TestStructuredHelmValuesEncodeAsOverrides(t *testing.T) {
	encoded, err := encodeHelmValueOverrides(map[string]interface{}{
		"previewId": "847",
		"images":    map[string]interface{}{"backend": map[string]interface{}{"tag": "preview-847"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded, "previewId: \"847\"") || !strings.Contains(encoded, "tag: preview-847") {
		t.Fatalf("unexpected Helm values YAML: %s", encoded)
	}
}

func TestSecretsAreWriteOnlyAndAttachToEventRules(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{Vault: vault})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"Registry password","environmentVariable":"REGISTRY_PASSWORD","value":"top-secret"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create secret: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("top-secret")) || bytes.Contains(response.Body.Bytes(), []byte("encryptedValue")) {
		t.Fatalf("secret value leaked in response: %s", response.Body.String())
	}
	var secret core.Secret
	if err := json.NewDecoder(response.Body).Decode(&secret); err != nil {
		t.Fatal(err)
	}
	if secret.Type != core.SecretTypeText {
		t.Fatalf("legacy create requests should default to text, got %q", secret.Type)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	triggerBody, _ := json.Marshal(map[string]any{"repository": "acme/credentials", "preDeployHook": "docker login", "secretIds": []string{secret.ID}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+overview.Apps[0].ID+"/event-triggers", bytes.NewReader(triggerBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create event trigger: %d %s", response.Code, response.Body.String())
	}
	var trigger core.EventTrigger
	if err := json.NewDecoder(response.Body).Decode(&trigger); err != nil {
		t.Fatal(err)
	}
	if len(trigger.SecretIDs) != 1 || trigger.SecretIDs[0] != secret.ID {
		t.Fatalf("secret binding was not returned: %#v", trigger)
	}
}

func TestIBMCloudSecretStoreAndExternalReferenceAreWriteOnly(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/identity/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "provider-token", "expiration": time.Now().Add(time.Hour).Unix()})
		case "/api/v2/secrets":
			if r.Header.Get("Authorization") != "Bearer provider-token" {
				t.Fatalf("provider request did not use the IAM token")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"secrets": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()

	storeBody, _ := json.Marshal(map[string]string{
		"name": "Production secrets", "provider": "ibm_cloud_secrets_manager", "serviceUrl": provider.URL, "iamUrl": provider.URL, "apiKey": "ibm-api-key",
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secret-stores", bytes.NewReader(storeBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create secret store: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("ibm-api-key")) || bytes.Contains(response.Body.Bytes(), []byte("encryptedCredentials")) {
		t.Fatalf("secret store credentials leaked: %s", response.Body.String())
	}
	var secretStore core.SecretStore
	if err := json.NewDecoder(response.Body).Decode(&secretStore); err != nil {
		t.Fatal(err)
	}
	if secretStore.State != "ready" || !secretStore.CredentialsConfigured {
		t.Fatalf("unexpected secret store: %#v", secretStore)
	}

	referenceBody, _ := json.Marshal(map[string]string{
		"name": "Registry token", "type": "registry_password", "source": "external", "environmentVariable": "REGISTRY_PASSWORD",
		"externalStoreId": secretStore.ID, "externalSecretId": "2c5d2ce4-88d7-4c4a-af13-0dbdb9450178", "externalField": "payload",
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewReader(referenceBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create external secret reference: %d %s", response.Code, response.Body.String())
	}
	var reference core.Secret
	if err := json.NewDecoder(response.Body).Decode(&reference); err != nil {
		t.Fatal(err)
	}
	if reference.Source != core.SecretSourceExternal || reference.ExternalStoreID != secretStore.ID || reference.EncryptedValue != "" {
		t.Fatalf("unexpected external secret reference: %#v", reference)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/secret-stores/"+secretStore.ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected referenced store deletion to fail, got %d: %s", response.Code, response.Body.String())
	}
}

func TestLanewayPrivateNetworkCanBeVerified(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "lanewayd.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	laneway := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/status":
			_, _ = w.Write([]byte(`{"running":true,"network_id":"network-1","name":"dispatch","selected_path":"wireguard-relay-quic","product_version":"1.0.0","controller":{"configuration_lease_expired":false}}`))
		case "/v1/routes":
			_, _ = w.Write([]byte(`[{"prefix":"10.40.0.0/16","via_node":"vpc","kind":"private"}]`))
		default:
			http.NotFound(w, r)
		}
	})}
	go func() { _ = laneway.Serve(listener) }()
	defer laneway.Close()
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	body, _ := json.Marshal(map[string]string{"name": "IBM VPC", "driver": "laneway", "socketPath": socketPath})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/private-networks", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create private network: %d %s", response.Code, response.Body.String())
	}
	var network core.PrivateNetwork
	if err := json.NewDecoder(response.Body).Decode(&network); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/private-networks/"+network.ID+"/verify", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("verify private network: %d %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&network); err != nil {
		t.Fatal(err)
	}
	if network.State != "ready" || network.Details["routeCount"] != "1" || network.Details["path"] != "wireguard-relay-quic" {
		t.Fatalf("unexpected private network: %#v", network)
	}
}

func TestLanewayConnectorRejectsUnsafeBootstrap(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/private-networks", bytes.NewBufferString(`{"name":"Dispatch network","driver":"laneway_connector","authority":"https://lane.example.com","route":"192.0.2.10"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create connector: %d %s", response.Code, response.Body.String())
	}
	var connector core.PrivateNetwork
	if err := json.NewDecoder(response.Body).Decode(&connector); err != nil {
		t.Fatal(err)
	}
	if connector.State != "waiting" || connector.Config["authority"] != "https://lane.example.com" || connector.Config["route"] != "192.0.2.10/32" {
		t.Fatalf("unexpected connector: %#v", connector)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/private-networks/"+connector.ID+"/install-connector", bytes.NewBufferString(`{"bootstrapCommand":"curl https://example.com/install.sh | sh"}`)))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsafe bootstrap: %d %s", response.Code, response.Body.String())
	}
}

func TestManagedEdgeNodeRoutesSecretStoreRequests(t *testing.T) {
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
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/private-networks", bytes.NewBufferString(`{"name":"IBM VPC","driver":"dispatch_agent"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create edge node: %d %s", response.Code, response.Body.String())
	}
	var node core.PrivateNetwork
	if err := json.NewDecoder(response.Body).Decode(&node); err != nil {
		t.Fatal(err)
	}
	if node.EnrollmentToken == "" || node.State != "waiting" {
		t.Fatalf("unexpected edge node: %#v", node)
	}
	token := node.EnrollmentToken

	agentDone := make(chan error, 1)
	go func() {
		for index := 0; index < 2; index++ {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/edge/nodes/"+node.ID+"/jobs/next", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			leaseResponse := httptest.NewRecorder()
			handler.ServeHTTP(leaseResponse, request)
			if leaseResponse.Code != http.StatusOK {
				agentDone <- fmt.Errorf("lease: %d %s", leaseResponse.Code, leaseResponse.Body.String())
				return
			}
			var job edge.LeasedJob
			if err := json.NewDecoder(leaseResponse.Body).Decode(&job); err != nil {
				agentDone <- err
				return
			}
			body := []byte(`{"secrets":[]}`)
			if strings.Contains(job.Request.URL, "/identity/token") {
				body = []byte(`{"access_token":"edge-token","expiration":4102444800}`)
			}
			completion, _ := json.Marshal(edge.Completion{LeaseToken: job.LeaseToken, Response: &edge.HTTPResponse{StatusCode: http.StatusOK, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: body}})
			completeRequest := httptest.NewRequest(http.MethodPost, "/api/v1/edge/nodes/"+node.ID+"/jobs/"+job.ID+"/complete", bytes.NewReader(completion))
			completeRequest.Header.Set("Authorization", "Bearer "+token)
			completeResponse := httptest.NewRecorder()
			handler.ServeHTTP(completeResponse, completeRequest)
			if completeResponse.Code != http.StatusNoContent {
				agentDone <- fmt.Errorf("complete: %d %s", completeResponse.Code, completeResponse.Body.String())
				return
			}
		}
		agentDone <- nil
	}()

	storeBody, _ := json.Marshal(map[string]string{
		"name": "VPC secrets", "provider": "ibm_cloud_secrets_manager", "serviceUrl": "https://private.secrets.example", "iamUrl": "https://private.iam.example", "apiKey": "key", "privateNetworkId": node.ID,
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secret-stores", bytes.NewReader(storeBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create routed secret store: %d %s", response.Code, response.Body.String())
	}
	if err := <-agentDone; err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	if strings.Contains(response.Body.String(), token) {
		t.Fatal("edge enrollment token was returned after creation")
	}
}

func TestGeneratedSSHSecretReturnsPublicMetadataOnly(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{Vault: vault})
	defer cleanup()

	response := httptest.NewRecorder()
	body := `{"name":"Global deploy key","type":"ssh_private_key","environmentVariable":"SSH_PRIVATE_KEY","generate":true}`
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("generate SSH secret: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("PRIVATE KEY")) || bytes.Contains(response.Body.Bytes(), []byte("encryptedValue")) {
		t.Fatalf("private SSH material leaked in response: %s", response.Body.String())
	}
	var secret core.Secret
	if err := json.NewDecoder(response.Body).Decode(&secret); err != nil {
		t.Fatal(err)
	}
	if secret.Type != core.SecretTypeSSHPrivateKey || !strings.HasPrefix(secret.PublicValue, "ssh-ed25519 ") {
		t.Fatalf("generated public key metadata missing: %#v", secret)
	}
}

func TestRelayServerAndProviderNeutralWebhookLifecycle(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	relayStore, err := relayservice.OpenStore(context.Background(), filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer relayStore.Close()
	relayServer := httptest.NewServer(relayservice.NewServer(relayStore, "this-is-a-long-relay-token", "https://relay.example.test"))
	defer relayServer.Close()
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()

	body, _ := json.Marshal(map[string]any{"name": "Public event relay", "runtime": "relay", "address": relayServer.URL, "relay": map[string]string{"accessToken": "this-is-a-long-relay-token"}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create relay: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("this-is-a-long-relay-token")) {
		t.Fatal("relay access token leaked")
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers/"+server.ID+"/relay/verify", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("verify relay: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers/"+server.ID+"/relay/webhooks", bytes.NewBufferString(`{"name":"Build events","provider":"build_system"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create relay webhook: %d %s", response.Code, response.Body.String())
	}
	var webhook core.RelayWebhook
	if err := json.NewDecoder(response.Body).Decode(&webhook); err != nil {
		t.Fatal(err)
	}
	if webhook.Provider != "build_system" || webhook.URL != "https://relay.example.test/hooks/"+webhook.RemoteID {
		t.Fatalf("unexpected webhook: %#v", webhook)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/servers/"+server.ID+"/relay/webhooks/"+webhook.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete relay webhook: %d %s", response.Code, response.Body.String())
	}
}

func TestGitHubAppCredentialsAreWriteOnly(t *testing.T) {
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
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	body, _ := json.Marshal(map[string]any{
		"name": "Engineering GitHub", "webUrl": "https://github.example.com", "appId": 42,
		"privateKey": privateKey, "webhookSecret": "0123456789abcdef",
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/github-apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create GitHub App: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("PRIVATE KEY")) || bytes.Contains(response.Body.Bytes(), []byte("0123456789abcdef")) || bytes.Contains(response.Body.Bytes(), []byte("encrypted")) {
		t.Fatalf("GitHub App credentials leaked in response: %s", response.Body.String())
	}
	var connection core.GitHubAppConnection
	if err := json.NewDecoder(response.Body).Decode(&connection); err != nil {
		t.Fatal(err)
	}
	if connection.APIURL != "https://github.example.com/api/v3" || connection.State != "needs_installation" || !connection.PrivateKeyConfigured || !connection.WebhookSecretConfigured {
		t.Fatalf("unexpected GitHub App connection: %#v", connection)
	}
	body, _ = json.Marshal(map[string]any{
		"name": "Polling GitHub", "webUrl": "https://github.example.com", "appId": 43,
		"privateKey": privateKey, "eventDelivery": "none",
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/github-apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create polling-only GitHub App: %d %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&connection); err != nil {
		t.Fatal(err)
	}
	if connection.WebhookURL != "" || connection.RelayWebhookID != "" || !connection.WebhookSecretConfigured {
		t.Fatalf("unexpected polling-only GitHub App: %#v", connection)
	}
}

func TestApplicationHooksStoreCredentialBindingsWithoutExposingValues(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{Vault: vault})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", bytes.NewBufferString(`{"name":"Build token","type":"api_token","environmentVariable":"BUILD_TOKEN","value":"hook-secret-value"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create hook secret: %d %s", response.Code, response.Body.String())
	}
	var secret core.Secret
	if err := json.NewDecoder(response.Body).Decode(&secret); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Apps) == 0 {
		t.Fatal("expected a demo application")
	}
	body, _ := json.Marshal(map[string]any{"preDeployHook": "echo build", "secretIds": []string{secret.ID}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/apps/"+overview.Apps[0].ID+"/hooks", bytes.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("update application hooks: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("hook-secret-value")) || bytes.Contains(response.Body.Bytes(), []byte("encrypted")) {
		t.Fatalf("hook credential leaked in response: %s", response.Body.String())
	}
	var app core.App
	if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	if app.PreDeployHook != "echo build" || len(app.HookSecretIDs) != 1 || app.HookSecretIDs[0] != secret.ID {
		t.Fatalf("unexpected hook configuration: %#v", app)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/secrets/"+secret.ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("delete bound hook secret: %d %s", response.Code, response.Body.String())
	}
}

func TestApplicationTemplateIsStoredWithoutDirectDeployment(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID, "serverId": overview.Servers[0].ID, "name": "PR preview",
		"buildType": "compose", "composeContent": "services:\n  app:\n    image: example/app", "template": true,
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create application template: %d %s", response.Code, response.Body.String())
	}
	var template core.App
	if err := json.NewDecoder(response.Body).Decode(&template); err != nil {
		t.Fatal(err)
	}
	if !template.Template || template.State != "template" {
		t.Fatalf("unexpected application template: %#v", template)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+template.ID+"/deployments", bytes.NewBufferString(`{"commitSha":"inline"}`)))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Template cannot be deployed")) {
		t.Fatalf("expected direct template deployment conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestPreviewGroupCRUDAndApplicationDeletionConflict(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{Vault: vault})
	defer cleanup()
	kubeconfig := testKubeconfig(t, "preview", "preview")

	response := httptest.NewRecorder()
	serverBody, _ := json.Marshal(map[string]any{"name": "group-cluster", "runtime": "kubernetes", "kubernetes": map[string]any{"kubeconfigPath": kubeconfig, "context": "preview"}})
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create Kubernetes server: %d %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appIDs := []string{}
	for _, name := range []string{"service", "ui"} {
		body, _ := json.Marshal(map[string]any{"projectId": overview.Projects[0].ID, "serverId": server.ID, "name": "group-" + name,
			"sourceRepo": "https://example.test/org/" + name, "buildType": "helm", "helmChart": "oci://charts/" + name, "domain": name + "-{preview}.example.test"})
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s app: %d %s", name, response.Code, response.Body.String())
		}
		var app core.App
		if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
			t.Fatal(err)
		}
		appIDs = append(appIDs, app.ID)
	}
	secretBody := bytes.NewBufferString(`{"name":"Group registry key","type":"registry_password","environmentVariable":"IBMCLOUD_API_KEY","value":"registry-secret"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/secrets", secretBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("create group hook secret: %d %s", response.Code, response.Body.String())
	}
	var secret core.Secret
	if err := json.NewDecoder(response.Body).Decode(&secret); err != nil {
		t.Fatal(err)
	}

	groupBody := map[string]any{"name": "full-stack", "command": "/preview", "components": []map[string]any{
		{"appId": appIDs[0], "alias": "service", "repository": "org/service", "defaultBranch": "main", "entrypoint": false, "dependsOn": []string{}, "secretIds": []string{secret.ID}},
		{"appId": appIDs[1], "alias": "ui", "repository": "org/ui", "defaultBranch": "main", "entrypoint": true, "dependsOn": []string{"service"}, "bindings": []map[string]string{{"source": "service.url", "helmValuePath": "config.backendUrl"}}},
	}}
	encoded, _ := json.Marshal(groupBody)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/preview-groups", bytes.NewReader(encoded)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create preview group: %d %s", response.Code, response.Body.String())
	}
	var group core.PreviewGroup
	if err := json.NewDecoder(response.Body).Decode(&group); err != nil {
		t.Fatal(err)
	}
	if len(group.Components[0].SecretIDs) != 1 || group.Components[0].SecretIDs[0] != secret.ID {
		t.Fatalf("group hook credential was not saved: %#v", group.Components[0])
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/secrets/"+secret.ID, nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected grouped hook secret deletion conflict, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/apps/"+appIDs[0], nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected grouped application deletion conflict, got %d: %s", response.Code, response.Body.String())
	}

	groupBody["name"] = "full-stack-preview"
	encoded, _ = json.Marshal(groupBody)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/preview-groups/"+group.ID, bytes.NewReader(encoded)))
	if response.Code != http.StatusOK {
		t.Fatalf("update preview group: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/preview-groups/"+group.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete preview group: %d %s", response.Code, response.Body.String())
	}
}

func TestApplicationRequiresReadyServer(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	serverBody := bytes.NewBufferString(`{"name":"remote-01","address":"10.0.0.8","runtime":"docker"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", serverBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.State != "pending" || server.AgentMode != "ssh-bootstrap" {
		t.Fatalf("expected remote server to require enrollment, got state=%s agentMode=%s", server.State, server.AgentMode)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId":  overview.Projects[0].ID,
		"serverId":   server.ID,
		"name":       "pending-app",
		"sourceRepo": "https://example.test/pending.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected pending target to return 409, got %d: %s", response.Code, response.Body.String())
	}
}

func TestHelmApplicationUsesReadyKubernetesServer(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	kubeconfig := testKubeconfig(t, "staging", "staging")

	serverBody, err := json.Marshal(map[string]any{
		"name": "preview-cluster", "runtime": "kubernetes",
		"kubernetes": map[string]any{"kubeconfigPath": kubeconfig, "context": "staging", "namespace": "previews"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Kubernetes server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID, "serverId": server.ID, "name": "preview-api", "buildType": "helm",
		"helmChart": "service", "helmRepository": "https://charts.example.test", "helmVersion": "1.2.3",
		"helmValues": "image:\n  tag: pr-42", "helmNamespace": "preview-42", "helmRelease": "preview-api-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Helm application creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var app core.App
	if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	if app.BuildType != core.BuildTypeHelm || app.HelmChart != "service" || app.HelmNamespace != "preview-42" {
		t.Fatalf("unexpected Helm application: %#v", app)
	}
	response = httptest.NewRecorder()
	valuesBody := `{"overrides":{"image":{"tag":"pr-84"},"replicaCount":3}}`
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/apps/"+app.ID+"/helm-values", strings.NewReader(valuesBody)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected Helm value update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	var saved struct {
		Overrides map[string]interface{} `json:"overrides"`
	}
	if err := json.NewDecoder(response.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	image, _ := saved.Overrides["image"].(map[string]interface{})
	if image["tag"] != "pr-84" {
		t.Fatalf("unexpected saved Helm overrides: %#v", saved.Overrides)
	}
}

func TestApplicationAcceptsPastedComposeWithoutRepository(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID,
		"serverId":  overview.Servers[0].ID,
		"name":      "pasted-compose",
		"composeContent": `services:
  app:
    image: ghcr.io/example/app:latest`,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected pasted Compose application to return 201, got %d: %s", response.Code, response.Body.String())
	}
	responseBody := response.Body.Bytes()
	var app core.App
	if err := json.Unmarshal(responseBody, &app); err != nil {
		t.Fatal(err)
	}
	if app.SourceRepo != "" || app.BuildType != core.BuildTypeCompose || app.ComposePath != "compose.yml" {
		t.Fatalf("unexpected pasted Compose application: %#v", app)
	}
	if bytes.Contains(responseBody, []byte("ghcr.io/example/app")) {
		t.Fatal("stored Compose content must not be returned by the API")
	}
}

func TestLocalControllerTargetIsManagedAutomatically(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	body := bytes.NewBufferString(`{"name":"local-docker","address":"LOCAL","runtime":"docker","agentMode":"ssh-bootstrap"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", body))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("managed automatically")) {
		t.Fatalf("expected manual local creation to return a managed-target conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestManagedLocalControllerCannotBeChangedOrDeleted(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	server := overview.Servers[0]
	if server.Address != "local" {
		t.Fatalf("expected demo controller target, got %#v", server)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewBufferString(`{"name":"renamed","address":"local"}`)))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Managed server cannot be changed")) {
		t.Fatalf("expected managed local update to return 409, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/servers/"+server.ID, nil))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Managed server cannot be deleted")) {
		t.Fatalf("expected managed local deletion to return 409, got %d: %s", response.Code, response.Body.String())
	}
}

func TestKubernetesServerCanBeConfiguredWithoutAnApplication(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	previewConfig := testKubeconfig(t, "preview", "preview")
	eastConfig := testKubeconfig(t, "east", "east")

	body, err := json.Marshal(map[string]any{
		"name": "Preview cluster", "runtime": "k8s",
		"kubernetes": map[string]any{"kubeconfigPath": " " + previewConfig + " ", "context": " preview ", "namespace": " pull-requests "},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Kubernetes server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Runtime != core.ServerRuntimeKubernetes || server.State != "ready" || server.AgentMode != "direct" {
		t.Fatalf("unexpected Kubernetes server state: %#v", server)
	}
	if server.Address != previewConfig || server.Kubernetes == nil || server.Kubernetes.Context != "preview" || server.Kubernetes.Namespace != "pull-requests" {
		t.Fatalf("unexpected Kubernetes server configuration: %#v", server)
	}

	update, err := json.Marshal(map[string]any{
		"name":       "Preview cluster east",
		"kubernetes": map[string]any{"kubeconfigPath": eastConfig, "context": "east"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewReader(update)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected Kubernetes server update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Address != eastConfig || server.Kubernetes == nil || server.Kubernetes.Namespace != "default" {
		t.Fatalf("unexpected updated Kubernetes server: %#v", server)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected overview to return 200, got %d", response.Code)
	}
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Apps) != 0 || len(overview.Servers) != 1 {
		t.Fatalf("Kubernetes server should not require an application: servers=%d apps=%d", len(overview.Servers), len(overview.Apps))
	}
}

func TestKubernetesServerRequiresMountedKubeconfigPath(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewBufferString(`{"name":"Cluster","runtime":"kubernetes","kubernetes":{}}`)))
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("kubeconfig path")) {
		t.Fatalf("expected missing kubeconfig path to return 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestOpenShiftServerRequiresSafeNonInteractiveOCLogin(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	for _, body := range []string{
		`{"name":"OpenShift","runtime":"openshift","kubernetes":{}}`,
		`{"name":"OpenShift","runtime":"openshift","kubernetes":{"loginCommand":"kubectl get pods"}}`,
		`{"name":"OpenShift","runtime":"openshift","kubernetes":{"loginCommand":"oc login https://api.example.test"}}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewBufferString(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected unsafe or interactive OpenShift login to return 400, got %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestOpenShiftRepairRejectsOtherServerTypes(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers/"+overview.Servers[0].ID+"/repair", bytes.NewBufferString(`{"loginCommand":"oc login --token=x"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected non-OpenShift repair conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestKubernetesServerStoresPastedKubeconfigAndCAWithoutReturningSecrets(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}))
	kubeconfig := `apiVersion: v1
kind: Config
current-context: preview
contexts:
  - name: preview
    context: {cluster: preview-cluster, user: preview-user}
clusters:
  - name: preview-cluster
    cluster:
      server: https://cluster.example.test
      certificate-authority: /local/ca.crt
users:
  - name: preview-user
    user: {token: private-cluster-token}
`
	body, err := json.Marshal(map[string]any{"name": "Stored cluster", "runtime": "kubernetes", "kubernetes": map[string]any{
		"source": "stored", "kubeconfig": kubeconfig, "certificateAuthority": ca, "namespace": "previews",
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("store Kubernetes server: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private-cluster-token")) || bytes.Contains(response.Body.Bytes(), []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("server response exposed stored credentials: %s", response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Address != "stored" || server.Kubernetes == nil || !server.Kubernetes.KubeconfigStored || !server.Kubernetes.CertificateAuthorityStored || server.Kubernetes.KubeconfigPath != "" || server.Kubernetes.Context != "preview" {
		t.Fatalf("unexpected stored Kubernetes server: %#v", server)
	}
	update, _ := json.Marshal(map[string]any{"name": "Stored cluster renamed", "kubernetes": map[string]any{
		"source": "stored", "context": "preview", "namespace": "previews",
	}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewReader(update)))
	if response.Code != http.StatusOK {
		t.Fatalf("update stored Kubernetes server: %d %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if !server.Kubernetes.KubeconfigStored || !server.Kubernetes.CertificateAuthorityStored {
		t.Fatalf("blank edit did not preserve stored credentials: %#v", server.Kubernetes)
	}
}

func TestApplicationRuntimeMustMatchServer(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	kubeconfig := testKubeconfig(t, "preview", "preview")
	serverBody, err := json.Marshal(map[string]any{
		"name": "preview-cluster", "runtime": "kubernetes",
		"kubernetes": map[string]any{"kubeconfigPath": kubeconfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Kubernetes server creation, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID, "serverId": server.ID, "name": "wrong-runtime",
		"composeContent": "services:\n  app:\n    image: example/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("Docker server")) {
		t.Fatalf("expected runtime mismatch to return 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestUndeployedApplicationCanBeDeletedBeforeServerAndProject(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	kubeconfig := testKubeconfig(t, "preview", "preview")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"name":"Preview"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected project creation, got %d: %s", response.Code, response.Body.String())
	}
	var project core.Project
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	serverBody, err := json.Marshal(map[string]any{
		"name": "preview-cluster", "runtime": "kubernetes",
		"kubernetes": map[string]any{"kubeconfigPath": kubeconfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId": project.ID, "serverId": server.ID, "name": "preview", "buildType": "helm",
		"helmChart": "oci://registry.example.test/charts/preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected app creation, got %d: %s", response.Code, response.Body.String())
	}
	var app core.App
	if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/v1/apps/" + app.ID, "/api/v1/servers/" + server.ID, "/api/v1/projects/" + project.ID} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodDelete, target, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("expected %s deletion to return 204, got %d: %s", target, response.Code, response.Body.String())
		}
	}
}

func TestSourceAuthenticationMatchesRepositoryTransport(t *testing.T) {
	for name, test := range map[string]struct {
		repository, authType, credentialID string
		valid                              bool
	}{
		"public HTTPS":   {repository: "https://github.com/example/charts.git", valid: true},
		"embedded token": {repository: "https://token@github.com/example/charts.git"},
		"public SSH":     {repository: "git@github.com:example/charts.git"},
		"token HTTPS":    {repository: "https://github.com/example/charts.git", authType: deploy.SourceAuthGitHubToken, credentialID: "token", valid: true},
		"key SSH":        {repository: "git@github.com:example/charts.git", authType: deploy.SourceAuthSSHKey, credentialID: "key", valid: true},
		"token SSH":      {repository: "git@github.com:example/charts.git", authType: deploy.SourceAuthGitHubToken, credentialID: "token"},
		"key HTTPS":      {repository: "https://github.com/example/charts.git", authType: deploy.SourceAuthSSHKey, credentialID: "key"},
		"missing key":    {repository: "git@github.com:example/charts.git", authType: deploy.SourceAuthSSHKey},
	} {
		t.Run(name, func(t *testing.T) {
			detail := validateSourceAuthentication(test.repository, test.authType, test.credentialID)
			if (detail == "") != test.valid {
				t.Fatalf("valid=%v detail=%q", test.valid, detail)
			}
		})
	}
}

func TestGeneratedSSHSecretExposesOnlyDerivedPublicKey(t *testing.T) {
	privateKey, publicKey, err := generateSSHKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(privateKey, "PRIVATE KEY") || strings.Contains(publicKey, "PRIVATE KEY") || !strings.HasPrefix(publicKey, "ssh-ed25519 ") {
		t.Fatalf("unexpected generated key material: private=%q public=%q", privateKey, publicKey)
	}
	derived, err := sshPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if derived != publicKey {
		t.Fatalf("derived public key differs: generated=%q derived=%q", publicKey, derived)
	}
}

func TestDeployedApplicationIsCleanedBeforeTransactionalDeletion(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "delete-project", Name: "Delete", CreatedAt: now}
	server := core.Server{ID: "delete-server", Name: "Delete", Address: "/config", Runtime: core.ServerRuntimeKubernetes, State: "ready", AgentMode: "direct", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config", Context: "test"}, CreatedAt: now}
	app := core.App{ID: "delete-app", ProjectID: project.ID, ServerID: server.ID, Name: "Delete", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/chart", State: "ready", CreatedAt: now}
	deployment := core.Deployment{ID: "delete-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if err := data.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: deployment.ID, Level: "info", Message: "live", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	executor := &observingCleanupExecutor{data: data}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, executor), false, AuthConfig{AdminToken: "secret"}, logger)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/apps/"+app.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected application deletion to return 204, got %d: %s", response.Code, response.Body.String())
	}
	if !executor.cleaned {
		t.Fatal("expected deployed resources to be cleaned before deletion")
	}
	if _, err := data.GetApp(ctx, app.ID); err != store.ErrNotFound {
		t.Fatalf("expected application record removed, got %v", err)
	}
	logs, err := data.ListDeploymentLogs(ctx, deployment.ID, 0)
	if err != nil || len(logs) != 0 {
		t.Fatalf("expected deployment history removed, got logs=%d err=%v", len(logs), err)
	}
}

func TestApplicationDeleteRejectsActivePreviewBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "preview-project", Name: "Preview delete", CreatedAt: now}
	server := core.Server{ID: "preview-server", Name: "Preview delete", Address: "/config", Runtime: core.ServerRuntimeKubernetes, State: "ready", AgentMode: "direct", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config", Context: "test"}, CreatedAt: now}
	app := core.App{ID: "preview-app", ProjectID: project.ID, ServerID: server.ID, Name: "Preview delete", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/chart", State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateDeployment(ctx, core.Deployment{ID: "preview-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	trigger := core.EventTrigger{ID: "preview-trigger", AppID: app.ID, Provider: core.EventProviderGitHub, Repository: "acme/preview", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if _, _, err := data.CreateEventTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	event := core.IncomingEvent{ID: "preview-event", Provider: core.EventProviderGitHub, DeliveryID: "preview-delivery", Kind: core.EventKindPullRequestComment, Action: "created", Repository: "acme/preview", PullRequestNumber: 9, Actor: "operator", ActorAssociation: "MEMBER", TrustedActor: true, Command: "/preview", ReceivedAt: now}
	result, err := data.ProcessIncomingEvent(ctx, event)
	if err != nil || len(result.Previews) != 1 {
		t.Fatalf("expected active preview, got %#v err=%v", result, err)
	}
	executor := &observingCleanupExecutor{data: data}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, executor), false, AuthConfig{AdminToken: "secret"}, logger)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/apps/"+app.ID, nil))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Close and clean every preview")) {
		t.Fatalf("expected active preview conflict, got %d: %s", response.Code, response.Body.String())
	}
	if executor.cleaned {
		t.Fatal("cleanup must not run before active preview validation")
	}
	if _, err := data.GetApp(ctx, app.ID); err != nil {
		t.Fatalf("preview template must be preserved: %v", err)
	}
}

type observingCleanupExecutor struct {
	data    store.Store
	cleaned bool
}

func (e *observingCleanupExecutor) Deploy(context.Context, core.Deployment, core.App, core.Server, deploy.Progress) error {
	return nil
}

func (e *observingCleanupExecutor) Cleanup(ctx context.Context, app core.App, _ core.Server, _ deploy.Progress) error {
	if _, err := e.data.GetApp(ctx, app.ID); err != nil {
		return err
	}
	e.cleaned = true
	return nil
}

func testKubeconfig(t *testing.T, currentContext string, contexts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	contents := "apiVersion: v1\nkind: Config\ncurrent-context: " + currentContext + "\ncontexts:\n"
	for _, contextName := range contexts {
		contents += "  - name: " + contextName + "\n    context:\n      cluster: cluster\n      user: user\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectAndServerCanBeEditedAndDeleted(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"name":"Platform","description":"Initial"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected project creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var project core.Project
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/projects/"+project.ID, bytes.NewBufferString(`{"name":"Services","description":"Updated"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected project update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.Name != "Services" || project.Description != "Updated" {
		t.Fatalf("unexpected updated project: %#v", project)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewBufferString(`{"name":"build-01","address":"10.0.0.8","runtime":"docker"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewBufferString(`{"name":"build-east","address":"10.0.0.9"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected server update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Name != "build-east" || server.Address != "10.0.0.9" {
		t.Fatalf("unexpected updated server: %#v", server)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/servers/"+server.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected server deletion to return 204, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/projects/"+project.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected project deletion to return 204, got %d: %s", response.Code, response.Body.String())
	}
}

func TestProjectAndServerDeleteRejectsReferencedResources(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/v1/projects/" + overview.Projects[0].ID, "/api/v1/servers/" + overview.Servers[0].ID} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodDelete, target, nil))
		if response.Code != http.StatusConflict {
			t.Fatalf("expected referenced resource deletion to return 409, got %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestFirstRunSetupSupportsBasicAuthentication(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"setupRequired":true`)) {
		t.Fatalf("expected setup to be required, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unconfigured controller to reject protected requests, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewBufferString(`{"username":"operator","password":"correct horse battery staple"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected setup to return 201, got %d: %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("operator", "correct horse battery staple")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected configured credentials to authenticate, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"operator","password":"correct horse battery staple"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected login to return 200, got %d: %s", response.Code, response.Body.String())
	}
	var session struct{ Token string }
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected session token to authenticate, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected logout to return 204, got %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected logout to revoke the session, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewBufferString(`{"username":"other","password":"another secure password"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected repeat setup to return 409, got %d", response.Code)
	}
}

func TestEnvironmentCredentialsUseBasicAuthentication(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{Username: "admin", Password: "environment password"})
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("admin", "environment password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected environment credentials to authenticate, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("admin", "wrong password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected wrong environment password to return 401, got %d", response.Code)
	}
}

func TestSignedPullRequestWebhookCreatesAndClosesPreview(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{WebhookSecret: "hook-secret", DefaultCommand: "/preview"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	triggerBody := bytes.NewBufferString(`{"repository":"acme/checkout"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+overview.Apps[0].ID+"/event-triggers", triggerBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected trigger creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.EventTriggers) != 1 || overview.EventTriggers[0].Repository != "acme/checkout" {
		t.Fatalf("expected overview event inventory, got %#v", overview.EventTriggers)
	}

	comment := []byte(`{"action":"created","repository":{"full_name":"acme/checkout"},"issue":{"number":17,"pull_request":{"url":"https://example.test/pulls/17"}},"comment":{"id":501,"body":"/preview","author_association":"MEMBER","user":{"login":"octo"}}}`)
	request := signedWebhookRequest("hook-secret", "issue_comment", "delivery-comment", comment)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected preview webhook to return 202, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, signedWebhookRequest("hook-secret", "issue_comment", "delivery-comment", comment))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"duplicate":true`)) {
		t.Fatalf("expected delivery retry to be idempotent, got %d: %s", response.Code, response.Body.String())
	}

	closed := []byte(`{"action":"closed","repository":{"full_name":"acme/checkout"},"sender":{"login":"octo"},"pull_request":{"number":17,"head":{"ref":"feature/cart","sha":"abc123"},"base":{"ref":"main"}}}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, signedWebhookRequest("hook-secret", "pull_request", "delivery-close", closed))
	if response.Code != http.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"state":"closed"`)) {
		t.Fatalf("expected close webhook to complete cleanup, got %d: %s", response.Code, response.Body.String())
	}
}

func TestEventTriggerDeploymentHooksCanBeUpdated(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{DefaultCommand: "/preview"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+overview.Apps[0].ID+"/event-triggers", bytes.NewBufferString(`{"repository":"acme/hooks","preDeployHook":"echo pre"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create event trigger: %d %s", response.Code, response.Body.String())
	}
	var trigger core.EventTrigger
	if err := json.NewDecoder(response.Body).Decode(&trigger); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/event-triggers/"+trigger.ID, bytes.NewBufferString(`{"command":"/ship-preview","enabled":true,"preDeployHook":"echo build","postDeployHook":"echo publish"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("update event trigger: %d %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Command != "/ship-preview" || trigger.PreDeployHook != "echo build" || trigger.PostDeployHook != "echo publish" {
		t.Fatalf("unexpected updated trigger: %#v", trigger)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{WebhookSecret: "hook-secret"})
	defer cleanup()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events/github", bytes.NewBufferString(`{}`))
	request.Header.Set("X-Hub-Signature-256", "sha256=00")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid signature to return 401, got %d", response.Code)
	}
}

func signedWebhookRequest(secret, event, delivery string, body []byte) *http.Request {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events/github", bytes.NewReader(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", delivery)
	return request
}

func tokenRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("Authorization", "Bearer secret")
	return request
}

func testHandler(t *testing.T, auth AuthConfig) (http.Handler, func()) {
	return testHandlerWithDemo(t, auth, true)
}

func testHandlerWithDemo(t *testing.T, auth AuthConfig, seedDemo bool) (http.Handler, func()) {
	return testHandlerWithEventConfig(t, auth, seedDemo, EventConfig{})
}

func testHandlerWithEventConfig(t *testing.T, auth AuthConfig, seedDemo bool, eventConfig EventConfig) (http.Handler, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		cancel()
		_ = data.Close()
		t.Fatal(err)
	}
	if seedDemo {
		if err := data.SeedDemo(ctx); err != nil {
			cancel()
			_ = data.Close()
			t.Fatal(err)
		}
	}
	if eventConfig.GitHubApps == nil && eventConfig.Vault != nil {
		eventConfig.GitHubApps = githubapp.New(data, eventConfig.Vault)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), seedDemo, auth, logger, eventConfig)
	return handler, func() { cancel(); _ = data.Close() }
}

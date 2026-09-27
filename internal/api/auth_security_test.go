package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
)

func authCall(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(body)))
	return response
}

func TestPasswordAttemptsShareLimitAcrossLoginAndBasic(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{Username: "operator", Password: "correct horse battery staple"}, false)
	defer cleanup()
	data := handler.(*API).store
	second := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), false, AuthConfig{Username: "operator", Password: "correct horse battery staple"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i < passwordAccountLimit; i++ {
		var request *http.Request
		if i%2 == 0 {
			request = httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"wrong password"}`))
		} else {
			request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
			request.SetBasicAuth("operator", "wrong password")
		}
		request.RemoteAddr = "192.0.2.11:1234"
		request.Header.Set("X-Forwarded-For", "198.51.100.5")
		response := httptest.NewRecorder()
		if i%2 == 0 {
			handler.ServeHTTP(response, request)
		} else {
			second.ServeHTTP(response, request)
		}
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, response.Code)
		}
	}
	response := authCall(second, http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"correct horse battery staple"}`)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
		t.Fatalf("expected login throttle, got %d: %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("operator", "correct horse battery staple")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("Basic auth bypassed throttle: %d", response.Code)
	}
}

func TestPublicDiscoveryDoesNotRevealAccountsAndIsLimited(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	api := handler.(*API)
	now := time.Now().UTC()
	if err := api.store.CreateUser(context.Background(), core.User{ID: "known", Username: "known", Email: "known@example.test", PasswordHash: "local-password-hash", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	provider := core.AuthProvider{ID: "github", Name: "Company GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.test", APIURL: "https://github.example.test/api/v3", ClientID: "client", Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now}
	if err := api.store.CreateAuthProvider(context.Background(), provider); err != nil {
		t.Fatal(err)
	}
	if err := api.store.UpsertExternalIdentity(context.Background(), core.ExternalIdentity{ProviderID: provider.ID, Subject: "subject", UserID: "known", Login: "known", Email: "known@example.test", CreatedAt: now, LastLogin: now}); err != nil {
		t.Fatal(err)
	}
	var first []byte
	for _, identifier := range []string{"known", "known@example.test", "nobody@example.test"} {
		response := authCall(handler, http.MethodPost, "/api/v1/auth/discover", `{"identifier":"`+identifier+`"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("discover %s: %d", identifier, response.Code)
		}
		if first == nil {
			first = bytes.Clone(response.Body.Bytes())
		} else if !bytes.Equal(first, response.Body.Bytes()) {
			t.Fatalf("discovery varies by account: %s vs %s", first, response.Body.Bytes())
		}
	}
	var discovery authDiscovery
	if err := json.Unmarshal(first, &discovery); err != nil {
		t.Fatal(err)
	}
	if discovery.Method != "choose" || !discovery.Password || len(discovery.Providers) != 1 {
		t.Fatalf("unusable discovery: %#v", discovery)
	}
	for i := 4; i <= publicClientLimit; i++ {
		response := authCall(handler, http.MethodPost, "/api/v1/auth/discover", `{"identifier":"unknown"}`)
		if response.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, response.Code)
		}
	}
	response := authCall(handler, http.MethodPost, "/api/v1/auth/discover", `{"identifier":"unknown"}`)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("discovery rate limit: %d", response.Code)
	}
}

func TestPasswordChangeRevokesSessionsAcrossControllers(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	first := handler.(*API)
	data := first.store
	second := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	create := httptest.NewRecorder()
	first.ServeHTTP(create, tokenRequest(http.MethodPost, "/api/v1/users", strings.NewReader(`{"username":"member","displayName":"Member","password":"original-password-123","systemRole":"member","state":"active"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("create member: %d %s", create.Code, create.Body.String())
	}
	login := authCall(first, http.MethodPost, "/api/v1/auth/login", `{"username":"member","password":"original-password-123"}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login: %d %s", login.Code, login.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	bearer := func(h http.Handler) int {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		request.Header.Set("Authorization", "Bearer "+session.Token)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, request)
		return response.Code
	}
	if bearer(first) != http.StatusOK || bearer(second) != http.StatusOK {
		t.Fatal("session was not shared")
	}
	change := httptest.NewRequest(http.MethodPut, "/api/v1/auth/password", strings.NewReader(`{"currentPassword":"original-password-123","newPassword":"replacement-password-123"}`))
	change.Header.Set("Authorization", "Bearer "+session.Token)
	response := httptest.NewRecorder()
	first.ServeHTTP(response, change)
	if response.Code != http.StatusNoContent {
		t.Fatalf("password change: %d %s", response.Code, response.Body.String())
	}
	if bearer(first) != http.StatusUnauthorized || bearer(second) != http.StatusUnauthorized {
		t.Fatal("old token survived password change")
	}
	fresh := authCall(second, http.MethodPost, "/api/v1/auth/login", `{"username":"member","password":"replacement-password-123"}`)
	if fresh.Code != http.StatusOK {
		t.Fatalf("new password rejected: %d %s", fresh.Code, fresh.Body.String())
	}
}

func TestClientAddressAndSecurityHeaders(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{PublicURL: "https://dispatch.example.test", TrustedProxyCIDRs: "10.0.0.0/8"}, false)
	defer cleanup()
	api := handler.(*API)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.RemoteAddr = "192.0.2.5:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.8")
	if actual := api.clientAddress(request); actual != "192.0.2.5" {
		t.Fatalf("untrusted proxy was accepted: %s", actual)
	}
	request.RemoteAddr = "10.0.0.4:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.3, 10.0.0.5")
	if actual := api.clientAddress(request); actual != "203.0.113.3" {
		t.Fatalf("trusted chain: %s", actual)
	}
	request.Header.Set("X-Forwarded-For", "198.51.100.9, 203.0.113.3, 10.0.0.5")
	if actual := api.clientAddress(request); actual != "203.0.113.3" {
		t.Fatalf("spoofed leftmost address accepted: %s", actual)
	}
	if got := api.oauthCallbackURL(request); got != "https://dispatch.example.test/api/v1/auth/callback" {
		t.Fatalf("callback origin: %s", got)
	}
	for _, path := range []string{"/", "/api/v1/auth/status", "/api/v1/auth/providers/does-not-exist/start"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Header().Get("Strict-Transport-Security") == "" || response.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(response.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("missing browser headers at %s: %v", path, response.Header())
		}
	}
}

func TestUntrustedForwardingCannotClaimHTTPS(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{}, false)
	defer cleanup()
	api := handler.(*API)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil)
	request.RemoteAddr = "192.0.2.10:1234"
	request.Header.Set("X-Forwarded-Proto", "https")
	if got := api.oauthCallbackURL(request); got != "http://example.com/api/v1/auth/callback" {
		t.Fatalf("trusted spoofed scheme: %s", got)
	}
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	if response.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HSTS set on untrusted HTTP")
	}
}

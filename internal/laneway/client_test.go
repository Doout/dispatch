package laneway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
)

func TestGenericApplicationAndAuthorizationURLs(t *testing.T) {
	if slices.Contains(NetworkManagementScopes, "node.manage") || !slices.Contains(NetworkManagementScopes, "enrollment.issue") {
		t.Fatalf("unexpected management scopes: %#v", NetworkManagementScopes)
	}
	action, err := NewApplicationAction("https://lane.example.com/base")
	if err != nil || action != "https://lane.example.com/base/applications/new" {
		t.Fatalf("unexpected application action: %s %v", action, err)
	}
	value, err := AuthorizationURL("https://lane.example.com/base", OAuthAuthorizationRequest{
		ClientID: "client-1", RedirectURI: "https://client.example.com/api/v1/laneway-networks/callback",
		State: "state", CodeChallenge: "challenge", Scopes: []string{"network.read", "node.read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/base/oauth/authorize" || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("unexpected URL: %s", value)
	}
	if parsed.Query().Get("client_id") != "client-1" || parsed.Query().Get("scope") != "network.read node.read" {
		t.Fatalf("unexpected query: %#v", parsed.Query())
	}
}

func TestValidateAuthorityRejectsURLMetadata(t *testing.T) {
	for _, value := range []string{
		"https://user@lane.example.com",
		"https://lane.example.com?next=other",
		"https://lane.example.com#fragment",
	} {
		if _, err := ValidateAuthority(value); err == nil {
			t.Fatalf("expected %q to fail", value)
		}
	}
}

func TestApplicationRegistrationAndOAuthTokenExchange(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/application-registrations/exchange":
			if r.Header.Get("Authorization") != "" {
				t.Fatal("application registration exchange must not use client credentials")
			}
			_ = json.NewEncoder(w).Encode(ApplicationRegistration{ApplicationID: "app-1", ClientID: "client-1", ClientSecret: "secret-1", Name: "Example client"})
		case "/oauth/token":
			clientID, clientSecret, ok := r.BasicAuth()
			if !ok || clientID != "client-1" || clientSecret != "secret-1" {
				t.Fatalf("unexpected client authentication: %q %q", clientID, clientSecret)
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code_verifier") != "verifier" {
				t.Fatalf("unexpected token request: %#v %v", r.Form, err)
			}
			_, _ = io.WriteString(w, `{"access_token":"access","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh","scope":"network.read","installation":{"installation_id":"install-1","application_id":"app-1","network":{"network_id":"network-1","name":"Production"}}}`)
		default:
			t.Fatalf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := Client{Authority: server.URL, HTTPClient: server.Client()}
	registration, err := client.ExchangeApplicationRegistration(context.Background(), ApplicationRegistrationRequest{Code: "register", CodeVerifier: "verifier"})
	if err != nil || registration.ClientID != "client-1" {
		t.Fatalf("registration: %#v %v", registration, err)
	}
	token, err := client.ExchangeOAuthCode(context.Background(), registration.ClientID, registration.ClientSecret, OAuthTokenRequest{Code: "authorize", CodeVerifier: "verifier", RedirectURI: "https://client.example.com/callback"})
	if err != nil || token.Installation.Network.ID != "network-1" || token.RefreshToken != "refresh" {
		t.Fatalf("token: %#v %v", token, err)
	}
}

func TestOAuthCodeExchangeRetriesGatewayFailures(t *testing.T) {
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access","token_type":"Bearer","installation":{"installation_id":"install-1","application_id":"app-1","network":{"network_id":"network-1","name":"Production"}}}`)
	}))
	defer server.Close()

	response, err := (Client{Authority: server.URL, HTTPClient: server.Client()}).ExchangeOAuthCode(context.Background(), "client", "secret", OAuthTokenRequest{Code: "code", CodeVerifier: "verifier", RedirectURI: "https://client.example.com/callback"})
	if err != nil || attempts != 3 || response.Installation.Network.ID != "network-1" {
		t.Fatalf("exchange after gateway failures: attempts=%d response=%#v err=%v", attempts, response, err)
	}
}

func TestOAuthCodeExchangeDoesNotRetryAuthorizationErrors(t *testing.T) {
	attempts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"client authentication failed"}`)
	}))
	defer server.Close()

	_, err := (Client{Authority: server.URL, HTTPClient: server.Client()}).ExchangeOAuthCode(context.Background(), "client", "secret", OAuthTokenRequest{Code: "code", CodeVerifier: "verifier", RedirectURI: "https://client.example.com/callback"})
	if err == nil || attempts != 1 {
		t.Fatalf("authorization error attempts=%d err=%v", attempts, err)
	}
}

func TestTokenRequestsDoNotFollowRedirects(t *testing.T) {
	redirectTargetCalled := false
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetCalled = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	_, err := (Client{Authority: server.URL, HTTPClient: server.Client()}).ExchangeOAuthCode(context.Background(), "client", "secret", OAuthTokenRequest{Code: "code", CodeVerifier: "verifier", RedirectURI: "https://client.example.com/callback"})
	if err == nil {
		t.Fatal("expected redirect response to fail")
	}
	if redirectTargetCalled {
		t.Fatal("token request followed a redirect")
	}
}

func TestOAuthTokenRevocationUsesClientAuthentication(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientID, clientSecret, ok := r.BasicAuth()
		if !ok || clientID != "client-1" || clientSecret != "secret-1" {
			t.Fatalf("unexpected client authentication: %q %q", clientID, clientSecret)
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("token") != "refresh-1" || r.Form.Get("token_type_hint") != "refresh_token" {
			t.Fatalf("unexpected revocation form: %#v %v", r.Form, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := (Client{Authority: server.URL, HTTPClient: server.Client()}).RevokeOAuthToken(context.Background(), "client-1", "secret-1", "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
}

func TestInventoryUsesScopedBearer(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Fatalf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v1/admin/networks/network-1":
			_ = json.NewEncoder(w).Encode(Network{ID: "network-1", Name: "Production", IPv4Pool: "100.64.0.0/24"})
		case "/v1/admin/networks/network-1/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []Node{{ID: "node-1", NetworkID: "network-1", Name: "vpc"}}})
		case "/v1/admin/networks/network-1/endpoint-statuses":
			_ = json.NewEncoder(w).Encode(map[string]any{"endpoint_statuses": []EndpointStatus{{NodeID: "node-1", Freshness: "current"}}})
		case "/v1/admin/networks/network-1/routes":
			_ = json.NewEncoder(w).Encode(map[string]any{"routes": []Route{{ID: "route-1", NetworkID: "network-1", NodeID: "node-1", Prefix: "10.0.0.0/8", State: "approved"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{Authority: server.URL, Token: "scoped-token", HTTPClient: server.Client()}
	inventory, err := client.Inventory(context.Background(), "network-1")
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Network.Name != "Production" || len(inventory.Nodes) != 1 || len(inventory.Routes) != 1 || len(inventory.EndpointStatuses) != 1 {
		t.Fatalf("unexpected inventory: %#v", inventory)
	}
}

func TestNodeInstallerAndRouteUseScopedManagementAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scoped-token" {
			t.Fatalf("missing bearer: %q", r.Header.Get("Authorization"))
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/v1/admin/networks/network-1/node-installers":
			if !bytes.Contains(body, []byte(`"install_mode":"docker_compose"`)) {
				t.Fatalf("unexpected installer request: %s", body)
			}
			_ = json.NewEncoder(w).Encode(NodeInstaller{ID: "install-1", Command: "docker compose up", ExpiresAtUnixSeconds: 10})
		case "/v1/admin/routes/assign":
			if !bytes.Contains(body, []byte(`"network_id":"network-1"`)) {
				t.Fatalf("unexpected route request: %s", body)
			}
			_ = json.NewEncoder(w).Encode(Route{ID: "route-1", NetworkID: "network-1", NodeID: "node-1", Prefix: "10.0.0.0/8", Mode: "nat"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := Client{Authority: server.URL, Token: "scoped-token", HTTPClient: server.Client()}
	installer, err := client.CreateNodeInstaller(context.Background(), "network-1", NodeInstallerRequest{Name: "vpc", Kind: "exit", InstallMode: "docker_compose"})
	if err != nil || installer.Command == "" {
		t.Fatalf("create installer: %#v %v", installer, err)
	}
	route, err := client.AssignRoute(context.Background(), "network-1", AssignRouteRequest{NodeID: "node-1", Prefix: "10.0.0.0/8", Mode: "nat"})
	if err != nil || route.ID != "route-1" {
		t.Fatalf("assign route: %#v %v", route, err)
	}
}

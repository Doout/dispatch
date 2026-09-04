package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/laneway"
	"github.com/doout/dispatch/internal/store"
)

func TestLanewayNetworkAuthorizationAndInventory(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	var registrationExchanged, tokenExchanged, revoked bool
	lanewayServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case http.MethodPost + " /v1/application-registrations/exchange":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(body, []byte(`"code":"registration-code"`)) || !bytes.Contains(body, []byte(`"code_verifier":"`)) {
				t.Fatalf("unexpected registration exchange: %s", body)
			}
			registrationExchanged = true
			_, _ = io.WriteString(w, `{"application_id":"application-1","client_id":"client-1","client_secret":"client-secret","name":"Dispatch"}`)
		case http.MethodPost + " /oauth/token":
			clientID, clientSecret, ok := r.BasicAuth()
			if !ok || clientID != "client-1" || clientSecret != "client-secret" {
				t.Fatalf("unexpected OAuth client authentication: %q %q", clientID, clientSecret)
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "authorization-code" || r.Form.Get("code_verifier") == "" {
				t.Fatalf("unexpected OAuth token exchange: %#v %v", r.Form, err)
			}
			tokenExchanged = true
			_, _ = io.WriteString(w, `{"access_token":"lnw_access_secret","token_type":"Bearer","expires_in":3600,"refresh_token":"lnw_refresh_secret","scope":"network.read node.read route.read","installation":{"installation_id":"installation-1","application_id":"application-1","network":{"network_id":"network-1","name":"Production","ipv4_pool":"10.42.0.0/16","configuration_epoch":4}}}`)
		case http.MethodGet + " /v1/admin/networks/network-1":
			assertLanewayBearer(t, r)
			_, _ = io.WriteString(w, `{"network_id":"network-1","name":"Production","ipv4_pool":"10.42.0.0/16","configuration_epoch":5}`)
		case http.MethodGet + " /v1/admin/networks/network-1/nodes":
			assertLanewayBearer(t, r)
			_, _ = io.WriteString(w, `{"nodes":[{"node_id":"node-1","network_id":"network-1","name":"vpc-exit","ipv4_address":"10.42.0.10","enrollment_class":"managed"}]}`)
		case http.MethodGet + " /v1/admin/networks/network-1/endpoint-statuses":
			assertLanewayBearer(t, r)
			_, _ = io.WriteString(w, `{"endpoint_statuses":[{"node_id":"node-1","network_id":"network-1","node_name":"vpc-exit","freshness":"current"}]}`)
		case http.MethodGet + " /v1/admin/networks/network-1/routes":
			assertLanewayBearer(t, r)
			_, _ = io.WriteString(w, `{"routes":[{"route_id":"route-1","network_id":"network-1","node_id":"node-1","prefix":"10.50.0.0/16","kind":"exit","mode":"managed","state":"ready"}]}`)
		case http.MethodPost + " /v1/admin/networks/network-1/node-installers":
			assertLanewayBearer(t, r)
			var input struct {
				InstallMode string `json:"install_mode"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.InstallMode != "systemd" {
				t.Fatalf("unexpected node installer request: %#v %v", input, err)
			}
			_, _ = io.WriteString(w, `{"installation_id":"installer-1","command":"sudo laneway node install lane.example.com --token-file ./laneway.code","expires_at_unix_seconds":100}`)
		case http.MethodPost + " /v1/admin/routes/assign":
			assertLanewayBearer(t, r)
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(body, []byte(`"network_id":"network-1"`)) {
				t.Fatalf("unexpected route request: %s", body)
			}
			_, _ = io.WriteString(w, `{"route_id":"route-2","network_id":"network-1","node_id":"node-1","prefix":"10.60.0.0/16","kind":"subnet","mode":"nat","state":"pending"}`)
		case http.MethodPost + " /oauth/revoke":
			clientID, clientSecret, ok := r.BasicAuth()
			if !ok || clientID != "client-1" || clientSecret != "client-secret" {
				t.Fatalf("unexpected revocation authentication: %q %q", clientID, clientSecret)
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("token") != "lnw_refresh_secret" || r.Form.Get("token_type_hint") != "refresh_token" {
				t.Fatalf("unexpected revocation request: %#v %v", r.Form, err)
			}
			revoked = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Laneway request %s %s", r.Method, r.URL.String())
		}
	}))
	defer lanewayServer.Close()

	previousTransport := http.DefaultTransport
	http.DefaultTransport = lanewayServer.Client().Transport
	defer func() { http.DefaultTransport = previousTransport }()

	databasePath := filepath.Join(t.TempDir(), "dispatch.db")
	handler, cleanup := newLanewayTestHandler(t, databasePath, vault)

	response := httptest.NewRecorder()
	body := `{"name":"Private services","authority":"` + lanewayServer.URL + `"}`
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/authorize", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("start Laneway authorization: %d %s", response.Code, response.Body.String())
	}
	var start struct {
		Method string            `json:"method"`
		Action string            `json:"action"`
		Fields map[string]string `json:"fields"`
	}
	if err := json.NewDecoder(response.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	registrationURL, err := url.Parse(start.Action)
	if err != nil {
		t.Fatal(err)
	}
	if start.Method != "post" || registrationURL.Path != "/applications/new" || start.Fields["state"] == "" || !strings.Contains(start.Fields["manifest"], `"name":"Dispatch"`) || strings.Contains(start.Action, "dispatch") {
		t.Fatalf("unexpected application registration: %#v", start)
	}
	if !strings.Contains(start.Fields["manifest"], `"setup_uri":"https://dispatch.example.com/api/v1/laneway-applications/setup"`) {
		t.Fatalf("manifest did not use the configured public URL: %s", start.Fields["manifest"])
	}
	cleanup()
	handler, cleanup = newLanewayTestHandler(t, databasePath, vault)

	response = httptest.NewRecorder()
	setupCallback := "/api/v1/laneway-applications/setup?state=" + url.QueryEscape(start.Fields["state"]) + "&code=registration-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, setupCallback, nil))
	if response.Code != http.StatusSeeOther || !registrationExchanged {
		t.Fatalf("complete Laneway application registration: %d %s %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	authorizationURL, err := url.Parse(response.Header().Get("Location"))
	if err != nil || authorizationURL.Path != "/oauth/authorize" || authorizationURL.Query().Get("client_id") != "client-1" || authorizationURL.Query().Get("state") == "" {
		t.Fatalf("unexpected network authorization URL: %s %v", response.Header().Get("Location"), err)
	}
	cleanup()
	handler, cleanup = newLanewayTestHandler(t, databasePath, vault)
	defer cleanup()

	response = httptest.NewRecorder()
	callback := "/api/v1/laneway-networks/callback?state=" + url.QueryEscape(authorizationURL.Query().Get("state")) + "&code=authorization-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, callback, nil))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "lanewayStatus=connected") || !tokenExchanged {
		t.Fatalf("complete Laneway authorization: %d %s %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/private-networks", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"driver":"laneway_network"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"lanewayApplicationId":`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"lanewayInstallationId":"installation-1"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"lanewayNetworkId":"network-1"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"credentialsConfigured":true`)) {
		t.Fatalf("saved Laneway network: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("lnw_access_secret")) || bytes.Contains(response.Body.Bytes(), []byte("lnw_refresh_secret")) || bytes.Contains(response.Body.Bytes(), []byte("client-secret")) || bytes.Contains(response.Body.Bytes(), []byte("encryptedCredentials")) {
		t.Fatalf("Laneway credential leaked: %s", response.Body.String())
	}
	var networks []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(bytes.NewReader(response.Body.Bytes())).Decode(&networks); err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 {
		t.Fatalf("expected one Laneway network, got %d", len(networks))
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/authorize", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("reuse Laneway application: %d %s", response.Code, response.Body.String())
	}
	var reused lanewayAuthorizationStart
	if err := json.NewDecoder(response.Body).Decode(&reused); err != nil {
		t.Fatal(err)
	}
	if reused.Method != "redirect" || !strings.Contains(reused.Action, "/oauth/authorize") || reused.Fields != nil {
		t.Fatalf("expected reusable application authorization, got %#v", reused)
	}
	reusedURL, err := url.Parse(reused.Action)
	if err != nil || reusedURL.Query().Get("state") == "" {
		t.Fatalf("unexpected reused authorization URL: %s %v", reused.Action, err)
	}
	response = httptest.NewRecorder()
	reusedCallback := "/api/v1/laneway-networks/callback?state=" + url.QueryEscape(reusedURL.Query().Get("state")) + "&code=authorization-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, reusedCallback, nil))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "lanewayStatus=connected") {
		t.Fatalf("reauthorize Laneway network: %d %s %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/private-networks", nil))
	var reauthorized []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&reauthorized); err != nil || len(reauthorized) != 1 || reauthorized[0].ID != networks[0].ID {
		t.Fatalf("reauthorization created another network: %#v %v", reauthorized, err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/laneway-networks/"+networks[0].ID+"/inventory", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"name":"vpc-exit"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"prefix":"10.50.0.0/16"`)) {
		t.Fatalf("Laneway inventory: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/"+networks[0].ID+"/node-installers", strings.NewReader(`{"name":"vpc-node","kind":"exit","installMode":"systemd"}`)))
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"command":"sudo laneway node install`)) {
		t.Fatalf("Laneway node installer: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/"+networks[0].ID+"/routes", strings.NewReader(`{"nodeId":"node-1","prefix":"10.60.0.0/16","mode":"nat","metric":100}`)))
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"route_id":"route-2"`)) {
		t.Fatalf("Laneway route assignment: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/private-networks/"+networks[0].ID, nil))
	if response.Code != http.StatusNoContent || !revoked {
		t.Fatalf("Laneway revocation: %d %s revoked=%t", response.Code, response.Body.String(), revoked)
	}
}

func TestLanewayApplicationRegistrationReportsUnsupportedServer(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	lanewayServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/application-registrations/exchange" {
			t.Fatalf("unexpected Laneway request %s %s", r.Method, r.URL.String())
		}
		http.NotFound(w, r)
	}))
	defer lanewayServer.Close()

	previousTransport := http.DefaultTransport
	http.DefaultTransport = lanewayServer.Client().Transport
	defer func() { http.DefaultTransport = previousTransport }()

	handler, cleanup := newLanewayTestHandler(t, filepath.Join(t.TempDir(), "dispatch.db"), vault)
	defer cleanup()

	response := httptest.NewRecorder()
	body := `{"name":"Private services","authority":"` + lanewayServer.URL + `"}`
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/authorize", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("start Laneway authorization: %d %s", response.Code, response.Body.String())
	}
	var start lanewayAuthorizationStart
	if err := json.NewDecoder(response.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	callback := "/api/v1/laneway-applications/setup?state=" + url.QueryEscape(start.Fields["state"]) + "&code=registration-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, callback, nil))
	if response.Code != http.StatusSeeOther {
		t.Fatalf("complete Laneway application registration: %d %s", response.Code, response.Body.String())
	}
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	want := "This Laneway server does not support application registration. Upgrade Laneway, then start the connection again."
	if location.Query().Get("lanewayStatus") != "error" || location.Query().Get("detail") != want {
		t.Fatalf("unexpected callback redirect: %s", location.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/private-networks", nil))
	if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "[]" {
		t.Fatalf("unsupported server saved a network: %d %s", response.Code, response.Body.String())
	}
}

func TestLanewayNetworkAuthorizationFailureReportsUpstreamStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "upstream HTTP status",
			err:  &laneway.HTTPError{StatusCode: http.StatusBadGateway, Message: "internal upstream detail"},
			want: "Laneway could not complete network authorization (502 Bad Gateway). Start the connection again.",
		},
		{
			name: "transport failure",
			err:  errors.New("connection reset"),
			want: "Laneway could not complete network authorization. Start the connection again.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := lanewayNetworkAuthorizationFailure(test.err); got != test.want {
				t.Fatalf("failure message = %q, want %q", got, test.want)
			}
		})
	}
}

func newLanewayTestHandler(t *testing.T, databasePath string, vault *secretcrypto.Vault) (http.Handler, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := store.Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		_ = data.Close()
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), false,
		AuthConfig{AdminToken: "secret", PublicURL: "https://dispatch.example.com"}, logger, EventConfig{Vault: vault})
	return handler, func() { _ = data.Close() }
}

func assertLanewayBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer lnw_access_secret" {
		t.Fatalf("unexpected Laneway authorization: %q", got)
	}
}

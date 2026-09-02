package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	secretcrypto "github.com/doout/dispatch/internal/crypto"
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

	var registrationExchanged, tokenExchanged bool
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
			_, _ = io.WriteString(w, `{"installation_id":"installer-1","command":"docker compose up","expires_at_unix_seconds":100}`)
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
		default:
			t.Fatalf("unexpected Laneway request %s %s", r.Method, r.URL.String())
		}
	}))
	defer lanewayServer.Close()

	previousTransport := http.DefaultTransport
	http.DefaultTransport = lanewayServer.Client().Transport
	defer func() { http.DefaultTransport = previousTransport }()

	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()

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

	response = httptest.NewRecorder()
	callback := "/api/v1/laneway-networks/callback?state=" + url.QueryEscape(authorizationURL.Query().Get("state")) + "&code=authorization-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, callback, nil))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "lanewayStatus=connected") || !tokenExchanged {
		t.Fatalf("complete Laneway authorization: %d %s %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/private-networks", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"driver":"laneway_network"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"credentialsConfigured":true`)) {
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

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/laneway-networks/"+networks[0].ID+"/inventory", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"name":"vpc-exit"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"prefix":"10.50.0.0/16"`)) {
		t.Fatalf("Laneway inventory: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/"+networks[0].ID+"/node-installers", strings.NewReader(`{"name":"vpc-node","kind":"exit","installMode":"docker_compose"}`)))
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"command":"docker compose up"`)) {
		t.Fatalf("Laneway node installer: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/laneway-networks/"+networks[0].ID+"/routes", strings.NewReader(`{"nodeId":"node-1","prefix":"10.60.0.0/16","mode":"nat","metric":100}`)))
	if response.Code != http.StatusCreated || !bytes.Contains(response.Body.Bytes(), []byte(`"route_id":"route-2"`)) {
		t.Fatalf("Laneway route assignment: %d %s", response.Code, response.Body.String())
	}
}

func assertLanewayBearer(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer lnw_access_secret" {
		t.Fatalf("unexpected Laneway authorization: %q", got)
	}
}

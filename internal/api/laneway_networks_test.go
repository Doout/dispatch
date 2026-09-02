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

	var exchanged bool
	lanewayServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case http.MethodPost + " /v1/integrations/dispatch/token":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(body, []byte(`"code":"authorization-code"`)) || !bytes.Contains(body, []byte(`"code_verifier":"`)) {
				t.Fatalf("unexpected token exchange: %s", body)
			}
			exchanged = true
			_, _ = io.WriteString(w, `{"access_token":"lnw_spat_v1_secret","token_type":"Bearer","principal_id":"principal-1","permissions":["network.read","node.read","route.read"],"network":{"network_id":"network-1","name":"Production","ipv4_pool":"10.42.0.0/16","configuration_epoch":4}}`)
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
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err := json.NewDecoder(response.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	authorizationURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	if authorizationURL.Path != "/integrations/dispatch/authorize" || authorizationURL.Query().Get("code_challenge_method") != "S256" || authorizationURL.Query().Get("state") == "" {
		t.Fatalf("unexpected authorization URL: %s", start.AuthorizationURL)
	}

	response = httptest.NewRecorder()
	callback := "/api/v1/laneway-networks/callback?state=" + url.QueryEscape(authorizationURL.Query().Get("state")) + "&code=authorization-code"
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, callback, nil))
	if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "lanewayStatus=connected") || !exchanged {
		t.Fatalf("complete Laneway authorization: %d %s %s", response.Code, response.Header().Get("Location"), response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/private-networks", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"driver":"laneway_network"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"credentialsConfigured":true`)) {
		t.Fatalf("saved Laneway network: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("lnw_spat_v1_secret")) || bytes.Contains(response.Body.Bytes(), []byte("encryptedCredentials")) {
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
	if got := r.Header.Get("Authorization"); got != "Bearer lnw_spat_v1_secret" {
		t.Fatalf("unexpected Laneway authorization: %q", got)
	}
}

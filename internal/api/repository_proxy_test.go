package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
)

func TestGitHubAppProxyIsScopedToTheConnection(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	makeProxy := func(name, auth string) *httptest.Server {
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != "github.example.test" || r.Header.Get("Proxy-Authorization") != auth {
				t.Errorf("unexpected GitHub request at %s proxy: %s", name, r.URL)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if r.URL.Path == "/app-manifests/code/conversions" {
				_, _ = w.Write([]byte(`{"id":27,"pem":"fixture"}`))
				return
			}
			_, _ = io.WriteString(w, name)
		}))
		t.Cleanup(proxy.Close)
		return proxy
	}
	controllerProxy := makeProxy("controller", "")
	appProxy := makeProxy("app", "Basic dXNlcjpmaXh0dXJlLXBhc3N3b3Jk")
	authenticated, err := url.Parse(appProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	authenticated.User = url.UserPassword("user", "fixture-password")
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()
	a := handler.(*API)
	serviceRequestTest(t, a, http.MethodPut, "/api/v1/settings/repository-proxy", map[string]string{"url": controllerProxy.URL, "noProxy": ""}, http.StatusOK)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	create := func(id int, name string, proxy map[string]string) core.GitHubAppConnection {
		t.Helper()
		payload := map[string]any{"name": name, "webUrl": "https://github.example.test", "appId": id, "privateKey": privateKey, "eventDelivery": "none"}
		if proxy != nil {
			payload["proxy"] = proxy
		}
		body := serviceRequestTest(t, a, http.MethodPost, "/api/v1/github-apps", payload, http.StatusCreated)
		var item core.GitHubAppConnection
		if err := json.Unmarshal(body, &item); err != nil || strings.Contains(string(body), "fixture-password") {
			t.Fatal("invalid or unredacted GitHub App connection", err)
		}
		return item
	}
	app := create(27, "Dedicated route", map[string]string{"url": authenticated.String()})
	inherited := create(28, "Inherited route", nil)
	if app.Proxy.URL != appProxy.URL {
		t.Fatal("App response did not include the sanitized proxy URL")
	}
	client := a.githubAppClient(app.ID)
	check := func(client *http.Client, want string) {
		t.Helper()
		response, err := client.Get("http://github.example.test/poll")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || string(body) != want {
			t.Fatalf("GitHub request used %q proxy, want %q: %v", body, want, err)
		}
	}
	check(client, "app")
	check(a.githubAppClient(inherited.ID), "controller")
	endpoint := "/api/v1/github-apps/" + app.ID
	serviceRequestTest(t, a, http.MethodPut, endpoint, map[string]any{"proxy": map[string]string{"noProxy": ".internal.test"}}, http.StatusOK)
	stored, err := a.store.GetGitHubApp(context.Background(), app.ID)
	if err != nil || stored.Proxy.EncryptedUserinfo == "" || stored.Proxy.NoProxy != ".internal.test" {
		t.Fatal("bypass edit lost the saved App proxy or credentials", err)
	}
	stored.APIURL = "http://github.example.test"
	stored.PrivateNetworkID = "saved-edge-route"
	result, err := a.eventConfig.GitHubApps.ConvertAppManifest(context.Background(), stored, "code")
	if err != nil || result.AppID != 27 {
		t.Fatalf("GitHub App request did not use the selected App proxy: %v", err)
	}
	serviceRequestTest(t, a, http.MethodPut, endpoint, map[string]any{"proxy": map[string]string{"url": ""}}, http.StatusOK)
	check(client, "controller")
}

func TestRepositoryProxySettingsRouteGitHubAndProtectCredentials(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Host != "github.example.test" ||
			r.URL.Path != "/app-manifests/code/conversions" || r.Header.Get("Proxy-Authorization") != "Basic YWRtaW46Zml4dHVyZS1wYXNzd29yZA==" {
			t.Errorf("unexpected request to the proxy: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"id":27,"pem":"fixture"}`))
	}))
	defer proxy.Close()
	address, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	authenticated := *address
	authenticated.User = url.UserPassword("admin", "fixture-password")
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()
	a := handler.(*API)
	const endpoint = "/api/v1/settings/repository-proxy"
	const github = "http://github.example.test"
	save := func(input string) string {
		t.Helper()
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, tokenRequest(http.MethodPut, endpoint, strings.NewReader(input)))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "fixture-password") || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("proxy save: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	input, err := json.Marshal(map[string]string{"url": authenticated.String(), "noProxy": ""})
	if err != nil {
		t.Fatal(err)
	}
	if saved := save(string(input)); !strings.Contains(saved, `"authenticated":true`) || !strings.Contains(saved, proxy.URL) {
		t.Fatalf("unexpected saved settings: %s", saved)
	}
	stored, err := a.store.GetRepositoryProxy(context.Background())
	if err != nil || stored.URL != proxy.URL || stored.EncryptedUserinfo == "" || strings.Contains(stored.EncryptedUserinfo, "fixture-password") {
		t.Fatal("proxy address and credentials were not stored separately", err)
	}
	result, err := a.eventConfig.GitHubApps.ConvertManifest(context.Background(), github, "code", "saved-edge-route")
	if err != nil || result.AppID != 27 {
		t.Fatalf("GitHub app request did not use the saved proxy: App ID %d, %v", result.AppID, err)
	}
	if saved := save(`{"noProxy":".example.test"}`); !strings.Contains(saved, `"authenticated":true`) {
		t.Fatalf("editing the bypass list changed credentials: %s", saved)
	}
	check := httptest.NewRequest(http.MethodGet, github, nil)
	transport := a.githubEventsHTTP.Transport.(*http.Transport)
	if selected, err := transport.Proxy(check); err != nil || selected != nil {
		t.Fatalf("bypass list did not apply: %v %v", selected, err)
	}
	if saved := save(`{"url":"","noProxy":""}`); !strings.Contains(saved, `"url":""`) || !strings.Contains(saved, `"authenticated":false`) {
		t.Fatalf("remove proxy: %s", saved)
	}
}

package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/publichttp"
)

func newGitHubAPIRootFixture(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("probe must request the API root without origin credentials")
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	return server
}

type githubTestProxy struct {
	*httptest.Server
	connects atomic.Int32
}

// Route CONNECT to the local TLS GitHub fixture regardless of external DNS.
func newGitHubTestProxy(t *testing.T, target *httptest.Server, useTLS bool, auth string) *githubTestProxy {
	t.Helper()
	proxy := &githubTestProxy{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "example.com:443" {
			http.Error(w, "unexpected test target", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Proxy-Authorization") != auth {
			// Enterprise proxies can use a custom reason phrase for a 407.
			client, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer client.Close()
			_, _ = io.WriteString(client, "HTTP/1.1 407 Company login required\r\nContent-Length: 0\r\n\r\n")
			return
		}
		proxy.connects.Add(1)
		upstream, err := net.DialTimeout("tcp", target.Listener.Addr().String(), 2*time.Second)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.Close()
		client, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		go func() { _, _ = io.Copy(upstream, rw) }()
		_, _ = io.Copy(client, upstream)
	})
	if useTLS {
		proxy.Server = httptest.NewTLSServer(handler)
	} else {
		proxy.Server = httptest.NewServer(handler)
	}
	t.Cleanup(proxy.Close)
	return proxy
}

func trustGitHubTestServer(a *API, target *httptest.Server) {
	manager := a.eventConfig.GitHubApps
	transport := a.repositoryProxy.Transport()
	transport.TLSClientConfig = target.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.DisableKeepAlives = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "example.com:443" {
			address = target.Listener.Addr().String()
		}
		host, _, _ := net.SplitHostPort(address)
		if !net.ParseIP(host).IsLoopback() {
			return nil, errors.New("fixture refused external network")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	manager.Client.Transport = transport
}

func TestGitHubAppConnectionPreflight(t *testing.T) {
	const probe = "/api/v1/github-apps/test-connection"
	vault := webhookTestVault(t)
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: vault})
	defer cleanup()
	a := handler.(*API)
	github := newGitHubAPIRootFixture(t)
	trustGitHubTestServer(a, github)
	httpProxy := newGitHubTestProxy(t, github, false, "")
	tlsProxy := newGitHubTestProxy(t, github, true, "Basic dXNlcjpwYXNzd29yZA==")
	proxyURL := strings.Replace(tlsProxy.URL, "://", "://user:password@", 1)
	settings, err := a.repositoryProxy.Prepare("fixture", proxyURL, "")
	if err != nil {
		t.Fatal(err)
	}
	app := core.GitHubAppConnection{ID: "fixture", Name: "fixture", WebURL: "https://example.com", APIURL: "https://example.com/api/v3", Proxy: settings, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := a.store.CreateGitHubApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, http.MethodPut, "/api/v1/settings/repository-proxy", map[string]string{"url": httpProxy.URL, "noProxy": ""}, 200)
	for _, input := range []map[string]any{
		{"webUrl": "https://example.com"},
		{"webUrl": "https://example.com", "proxy": map[string]string{"url": proxyURL}},
		{"id": "fixture", "proxy": map[string]string{"noProxy": ""}},
		{"id": "fixture", "proxy": map[string]string{"url": ""}},
		{"id": "fixture", "proxy": map[string]string{"url": "http://bad.invalid", "noProxy": "example.com"}},
	} {
		body := serviceRequestTest(t, a, http.MethodPost, probe, input, 200)
		if strings.TrimSpace(string(body)) != `{"reachable":true}` {
			t.Fatalf("unexpected result %s", body)
		}
	}
	if httpProxy.connects.Load() != 2 || tlsProxy.connects.Load() != 2 {
		t.Fatalf("proxy inheritance, credential preservation or bypass failed: HTTP CONNECTs=%d HTTPS CONNECTs=%d", httpProxy.connects.Load(), tlsProxy.connects.Load())
	}
	saved, err := a.store.GetGitHubApp(context.Background(), app.ID)
	if err != nil || saved.Proxy != app.Proxy {
		t.Fatal("probe modified saved settings")
	}
	serviceRequestTest(t, a, http.MethodPost, probe, map[string]any{"apiUrl": "https://example.com/api/v3?password=fixture"}, 400)
	serviceRequestTest(t, a, http.MethodPost, probe, map[string]any{"privateNetworkId": "missing-edge"}, 400)
	failure := serviceRequestTest(t, a, http.MethodPost, probe, map[string]any{"id": "fixture", "proxy": map[string]string{"url": tlsProxy.URL}}, 502)
	if !strings.Contains(strings.ToLower(string(failure)), "proxy authentication") {
		t.Fatalf("missing proxy diagnosis: %s", failure)
	}

	transport := a.eventConfig.GitHubApps.Client.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}
	a.eventConfig.GitHubApps.Client.Transport = transport
	failure = serviceRequestTest(t, a, http.MethodPost, "/api/v1/github-apps/manifest", map[string]any{
		"name": "fixture", "webUrl": "https://example.com", "ownerType": "personal", "eventDelivery": "relay", "relayServerId": "must-never-be-looked-up", "proxy": map[string]string{"url": proxyURL}}, 502)
	if !strings.Contains(string(failure), "does not trust the HTTPS proxy certificate") || strings.Contains(string(failure), "password") || len(a.manifestStates) != 0 {
		t.Fatalf("unexpected failed preflight %s", failure)
	}

	failure = serviceRequestTest(t, a, http.MethodPost, probe, map[string]any{"webUrl": "https://example.com"}, 502)
	if !strings.Contains(string(failure), "GitHub API certificate") || !strings.Contains(string(failure), "TLS-inspection CA") {
		t.Fatalf("missing destination certificate diagnosis: %s", failure)
	}
	// The hosted manager's DNS policy must remain in charge of outbound dials.
	a.eventConfig.GitHubApps.Client = publichttp.Client()
	failure = serviceRequestTest(t, a, http.MethodPost, probe, map[string]any{"webUrl": github.URL}, 502)
	if !strings.Contains(string(failure), "enrolled edge node") {
		t.Fatalf("hosted probe did not enforce public DNS policy: %s", failure)
	}
}

func TestGitHubAppProbeIgnoresRedirectsAndResponseBodies(t *testing.T) {
	github := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect/":
			http.Redirect(w, r, "/escaped-target", http.StatusFound)
		case "/success/":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/unavailable/":
			http.Error(w, "sensitive fixture details", http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected preflight destination %s", r.URL.Path)
		}
	}))
	t.Cleanup(github.Close)
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Vault: webhookTestVault(t)})
	defer cleanup()
	a := handler.(*API)
	trustGitHubTestServer(a, github)
	for _, tc := range []struct {
		path    string
		status  int
		message string
	}{
		{"success", 200, "reachable"}, {"redirect", 502, "redirected"}, {"unavailable", 502, "HTTP 503"},
	} {
		result := string(serviceRequestTest(t, a, http.MethodPost, "/api/v1/github-apps/test-connection", map[string]string{"apiUrl": github.URL + "/" + tc.path}, tc.status))
		if !strings.Contains(result, tc.message) || strings.Contains(result, "sensitive fixture details") {
			t.Errorf("unexpected %s result %s", tc.path, result)
		}
	}
}

package uipreview

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func previewRequest(method, path, body string, production bool) *http.Request {
	r := httptest.NewRequest(method, "https://dev.example.test"+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://dev.example.test")
	r.Header.Set("Content-Type", "application/json")
	if production {
		r.AddCookie(&http.Cookie{Name: sourceCookie, Value: "production"})
	}
	return r
}

func fixture(t *testing.T, handler http.Handler) (*Server, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var devCalls atomic.Int32
	development := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		devCalls.Add(1)
		fmt.Fprint(w, `{"backend":"development"}`)
	}))
	production := httptest.NewTLSServer(handler)
	t.Cleanup(development.Close)
	t.Cleanup(production.Close)
	s, err := New(Config{Origin: "https://dev.example.test", Development: development.URL, Production: production.URL})
	if err != nil {
		t.Fatal(err)
	}
	s.transport = production.Client().Transport
	return s, production, &devCalls
}

func TestSourcesAndSessionIsolation(t *testing.T) {
	var calls atomic.Int32
	s, production, devCalls := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Origin") != "https://"+r.Host {
			t.Error("wrong upstream origin")
		}
		if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("untrusted forwarding headers passed through")
		}
		if r.URL.Path == "/api/v1/account/login" {
			http.SetCookie(w, &http.Cookie{Name: "__Host-dispatch-platform", Value: "production-session", Secure: true, HttpOnly: true, Path: "/"})
		} else {
			cookie, err := r.Cookie("__Host-dispatch-platform")
			if err != nil || cookie.Value != "production-session" {
				t.Error("production session missing")
			}
			if len(r.Cookies()) != 1 {
				t.Error("local session or source cookie leaked upstream")
			}
		}
		fmt.Fprint(w, `{"backend":"production"}`)
	}))
	r := previewRequest("GET", "/api/v1/account", "", false)
	r.AddCookie(&http.Cookie{Name: "__Host-dispatch-platform", Value: "development-session"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || devCalls.Load() != 1 || calls.Load() != 0 {
		t.Fatal("default must use development")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("development API responses must not survive a source switch in browser caches")
	}
	r = previewRequest("POST", "/api/v1/account/login", `{"email":"test@example.test","password":"test-password"}`, true)
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("Forwarded", "host=attacker.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if w.Code != 200 || len(cookies) != 1 || !strings.HasPrefix(cookies[0].Name, productionCookiePrefix) || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].Domain != "" {
		t.Fatal("production cookie was not isolated")
	}
	r = previewRequest("GET", "/api/v1/account", "", true)
	r.AddCookie(cookies[0])
	r.AddCookie(&http.Cookie{Name: "__Host-dispatch-platform", Value: "development-session"})
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || calls.Load() != 2 {
		t.Fatalf("production read failed: %d", w.Code)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("production API response could be cached")
	}
	if s.production.String() != production.URL {
		t.Fatal("configured upstream changed")
	}
}

func TestProductionWritesNeverReachBackend(t *testing.T) {
	var calls atomic.Int32
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	for _, item := range []struct{ method, path string }{
		{"POST", "/api/v1/platform/tenants"}, {"PUT", "/api/v1/account/password"},
		{"DELETE", "/api/v1/tenants/tenant/members/user"}, {"PATCH", "/api/v1/projects/project"},
		{"POST", "/api/v1/deployments/deployment/run"}, {"POST", "/api/v1/account/%6cogin"},
		{"POST", "/api/v1/account/login/../platform/tenants"}, {"OPTIONS", "/api/v1/account"},
		{"GET", "/api/v1/github-apps/manifest/callback"}, {"GET", "/api/v1/auth/providers/manifest/callback"},
		{"GET", "/api/v1/auth/callback"}, {"GET", "/api/v1/laneway-applications/setup"}, {"GET", "/api/v1/laneway-networks/callback"},
		{"GET", "/api/v1/edge/nodes/node/jobs/next"}, {"GET", "/api/v1/edge/nodes/node/runtime/jobs/next"},
		{"HEAD", "/api/v1/edge/nodes/node/jobs/next"}, {"GET", "/api/v1/edge/nodes/node/jobs/%6eext"},
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, previewRequest(item.method, item.path, `{}`, true))
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", item.method, item.path, w.Code)
		}
	}
	r := previewRequest("GET", "/api/v1/agents/connect", "", true)
	r.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 || calls.Load() != 0 {
		t.Fatal("write or interactive connection reached production")
	}
}

func TestSourceSwitchAndOriginBoundaries(t *testing.T) {
	var calls atomic.Int32
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	for _, source := range []string{"production", "development"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, previewRequest("POST", sourcePath, `{"source":"`+source+`"}`, false))
		cookies := w.Result().Cookies()
		if w.Code != 204 || len(cookies) != 1 || cookies[0].Value != source || cookies[0].Domain != "dev.example.test" || !cookies[0].HttpOnly || !cookies[0].Secure {
			t.Fatal("invalid data source switch")
		}
	}
	for _, body := range []string{`{"source":"https://arbitrary.example"}`, `{"source":"production","upstream":"https://arbitrary.example"}`, `{"source":"production"}{}`} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, previewRequest("POST", sourcePath, body, false))
		if w.Code != 400 || len(w.Result().Cookies()) != 0 {
			t.Fatal("source must be fixed configuration")
		}
	}
	for _, origin := range []string{"https://attacker.example", ""} {
		r := previewRequest("POST", sourcePath, `{"source":"production"}`, false)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("source switch accepted a foreign or absent origin")
		}
	}
	r := previewRequest("POST", "/api/v1/account/login", `{}`, true)
	r.Header.Set("Origin", "https://attacker.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 || calls.Load() != 0 {
		t.Fatal("foreign origin reached production")
	}
	r = previewRequest("GET", "/api/v1/account", "", true)
	r.Host = "unrelated.example"
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 404 || calls.Load() != 0 {
		t.Fatal("unrelated host reached production")
	}
}

func TestProductionNavigationRewritesOnlyKnownFields(t *testing.T) {
	var backend string
	s, production, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/hosted":
			fmt.Fprintf(w, `{"mode":"platform","origin":%q,"registrationEnabled":false}`, backend)
		case "/api/v1/account/tenants":
			fmt.Fprintf(w, `{"memberships":[{"url":%q,"tenant":{"name":%q}}],"invitations":[]}`, strings.Replace(backend, "://", "://team.", 1)+"/api/v1/hosted/auth/start", backend)
		case "/api/v1/account/handoffs":
			fmt.Fprintf(w, `{"url":%q}`, strings.Replace(backend, "://", "://team.", 1)+"/#tenant_code=one-time-code")
		}
	}))
	backend = production.URL
	for _, path := range []string{"/api/v1/hosted", "/api/v1/account/tenants", "/api/v1/account/handoffs"} {
		method := "GET"
		if strings.HasSuffix(path, "handoffs") {
			method = "POST"
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, previewRequest(method, path, `{}`, true))
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		var data map[string]any
		if json.Unmarshal(w.Body.Bytes(), &data) != nil {
			t.Fatal("invalid rewritten JSON")
		}
		if path == "/api/v1/hosted" && (data["origin"] != "https://dev.example.test" || data["dataOrigin"] != backend) {
			t.Fatal("origin or display address incorrect")
		}
		if path == "/api/v1/account/tenants" {
			item := data["memberships"].([]any)[0].(map[string]any)
			if item["url"] != "https://team.dev.example.test/api/v1/hosted/auth/start" || item["tenant"].(map[string]any)["name"] != backend {
				t.Fatal("navigation incorrect or unrelated data modified")
			}
		}
		if path == "/api/v1/account/handoffs" && data["url"] != "https://team.dev.example.test/#tenant_code=one-time-code" {
			t.Fatal("handoff left preview namespace")
		}
	}
	for _, raw := range []string{"https://outside.example/path", "https://nested.team.127.0.0.1/path", "https://127.0.0.1.evil.example/path"} {
		if s.localURL(raw) != raw {
			t.Fatal("unrelated destination was rewritten")
		}
	}
}

func TestTenantHostAndCookieMapping(t *testing.T) {
	s, production, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Host != "team."+strings.TrimPrefix(production.URL, "https://") || r.Header.Get("Origin") != "https://"+r.Host {
			t.Error("tenant host or origin not mapped")
		}
		cookie, err := r.Cookie("__Host-dispatch-tenant")
		if err != nil || cookie.Value != "tenant-session" {
			t.Error("tenant session not forwarded")
		}
		return &http.Response{StatusCode: 303, Header: http.Header{"Location": []string{production.URL + "/?tenant=id&challenge=pkce"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	r := previewRequest("POST", "/api/v1/hosted/auth/exchange", `{}`, true)
	r.Host = "team.dev.example.test"
	r.Header.Set("Origin", "https://team.dev.example.test")
	r.AddCookie(&http.Cookie{Name: productionCookiePrefix + base64.RawURLEncoding.EncodeToString([]byte("__Host-dispatch-tenant")), Value: "tenant-session"})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 303 || w.Header().Get("Location") != "https://dev.example.test/?tenant=id&challenge=pkce" {
		t.Fatal("tenant sign-in redirect left preview")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTMLUsesDevelopmentAssetsAndPreviewMetadata(t *testing.T) {
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("UI request reached backend") }))
	for _, production := range []bool{false, true} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, previewRequest("GET", "/?view=catalog", "", production))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `id="dispatch-ui-preview"`) || !strings.Contains(w.Body.String(), `id="root"`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("UI did not contain development assets and source metadata")
		}
		if production && !strings.Contains(w.Body.String(), `"readOnly":true`) {
			t.Fatal("read-only state missing")
		}
	}
}

func TestLogStreamsFlushBeforeUpstreamCloses(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	proxy := httptest.NewServer(s)
	defer proxy.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	r, _ := http.NewRequest("GET", proxy.URL+"/api/v1/workflow/revisions/run/logs/watch", nil)
	r.Host = "dev.example.test"
	r.AddCookie(&http.Cookie{Name: sourceCookie, Value: "production"})
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	first := make([]byte, len("data: first\n\n"))
	if _, err = io.ReadFull(response.Body, first); err != nil {
		t.Fatal(err)
	}
	if string(first) != "data: first\n\n" {
		t.Fatal("live output changed")
	}
}

func TestConfigRejectsUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"http://dev.example.test", "https://user:password@dev.example.test", "https://dev.example.test/path"} {
		if _, err := New(Config{Origin: origin, Development: "http://127.0.0.1:8081", Production: "https://prod.example.test"}); err == nil {
			t.Fatal("accepted invalid preview origin")
		}
	}
	for _, upstream := range []string{"http://prod.example.test", "https://user:password@prod.example.test", "https://prod.example.test?token=secret"} {
		if _, err := New(Config{Origin: "https://dev.example.test", Development: "http://127.0.0.1:8081", Production: upstream}); err == nil {
			t.Fatal("accepted invalid production origin")
		}
	}
}

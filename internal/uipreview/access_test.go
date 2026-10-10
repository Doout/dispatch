package uipreview

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccessKeyRequiredAndUserSessionCannotBypass(t *testing.T) {
	for _, key := range []string{"", "short"} {
		if _, err := New(Config{Origin: "https://dev.example.test", Development: "http://127.0.0.1:8081", Production: "https://prod.example.test", AccessKey: key}); err == nil {
			t.Fatal("started without a sufficient access key")
		}
	}
	var calls atomic.Int32
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	for _, item := range []struct{ method, path, body string }{
		{"GET", "/api/v1/account", ""},
		{"GET", "/api/v1/platform/tenants", ""},
		{"POST", "/api/v1/account/login", `{}`},
		{"POST", sourcePath, `{"source":"production"}`},
	} {
		r := previewRequest(item.method, item.path, item.body, true)
		r.AddCookie(&http.Cookie{Name: productionCookiePrefix + "X19Ib3N0LWRpc3BhdGNoLXBsYXRmb3Jt", Value: "valid-user-session"})
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 || len(w.Result().Cookies()) != 0 {
			t.Fatal("user session or source cookie bypassed key", w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("locked request reached production")
	}
}

func TestUnlockExpiryForgeryAndRotation(t *testing.T) {
	var calls atomic.Int32
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := r.Cookie(accessCookie); err == nil {
			t.Error("preview access cookie leaked upstream")
		}
		w.WriteHeader(200)
	}))
	for _, body := range []string{`{"key":"wrong"}`, `{"key":"wrong","extra":true}`, `{"key":"wrong"}{}`} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, previewRequest("POST", accessPath, body, false))
		if w.Code < 400 || len(w.Result().Cookies()) != 0 || strings.Contains(w.Body.String(), "wrong") {
			t.Fatal("invalid key was accepted or reflected")
		}
	}
	body, _ := json.Marshal(map[string]string{"key": testAccessKey})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, previewRequest("POST", accessPath, string(body), false))
	cookies := w.Result().Cookies()
	if w.Code != 204 || len(cookies) != 1 {
		t.Fatal("unlock failed", w.Code)
	}
	cookie := cookies[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Domain != "dev.example.test" || cookie.MaxAge != 3600 || strings.Contains(cookie.Value, testAccessKey) {
		t.Fatal("unsafe unlock cookie")
	}
	request := func(c *http.Cookie) *httptest.ResponseRecorder {
		r := previewRequest("GET", "/api/v1/account", "", true)
		r.AddCookie(c)
		response := httptest.NewRecorder()
		s.ServeHTTP(response, r)
		return response
	}
	if request(cookie).Code != 200 || calls.Load() != 1 {
		t.Fatal("valid grant did not allow production")
	}
	forged := *cookie
	forged.Value = strings.Replace(cookie.Value, ".", "0.", 1)
	if request(&forged).Code != 403 || calls.Load() != 1 {
		t.Fatal("forged grant accepted")
	}
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if request(cookie).Code != 403 || calls.Load() != 1 {
		t.Fatal("expired grant accepted")
	}
	s.now = time.Now
	rotated, err := New(Config{Origin: s.origin.String(), Development: s.development.String(), Production: s.production.String(), AccessKey: testAccessKey + "-rotated"})
	if err != nil {
		t.Fatal(err)
	}
	r := previewRequest("GET", "/api/v1/account", "", true)
	r.AddCookie(cookie)
	response := httptest.NewRecorder()
	rotated.ServeHTTP(response, r)
	if response.Code != 403 {
		t.Fatal("old grant survived key rotation")
	}
	r = previewRequest("GET", "/api/v1/account", "", true)
	r.Host = "team.dev.example.test"
	r.Header.Set("Origin", "https://team.dev.example.test")
	r.AddCookie(cookie)
	if _, valid := s.productionAccess(r); !valid {
		t.Fatal("grant did not cover tenant navigation")
	}
}

func TestUnlockOriginLockAndSecretNeverInHTML(t *testing.T) {
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	body, _ := json.Marshal(map[string]string{"key": testAccessKey})
	for _, origin := range []string{"", "https://attacker.example"} {
		r := previewRequest("POST", accessPath, string(body), false)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 || len(w.Result().Cookies()) != 0 {
			t.Fatal("cross-origin unlock accepted")
		}
	}
	r := previewRequest("DELETE", accessPath, "", true)
	cookie, _ := s.newAccessCookie()
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 || len(w.Result().Cookies()) != 2 {
		t.Fatal("lock failed")
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == accessCookie && c.MaxAge != -1 {
			t.Fatal("grant cookie not deleted")
		}
		if c.Name == sourceCookie && c.Value != "development" {
			t.Fatal("lock did not return to dev")
		}
	}
	w = httptest.NewRecorder()
	s.ServeHTTP(w, previewRequest("GET", "/", "", true))
	if strings.Contains(w.Body.String(), testAccessKey) || !strings.Contains(w.Body.String(), `"productionAccess":false`) {
		t.Fatal("HTML disclosed key or treated a locked session as unlocked")
	}
}

func TestAccessExpiryClosesLiveStreams(t *testing.T) {
	s, _, _ := fixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	proxy := httptest.NewServer(s)
	defer proxy.Close()
	expiry := time.Now().Add(2 * time.Second).Unix()
	payload := strconv.FormatInt(expiry, 10) + ".stream-test-nonce"
	value := payload + "." + base64.RawURLEncoding.EncodeToString(s.accessMAC(payload))
	r, _ := http.NewRequest("GET", proxy.URL+"/api/v1/workflow/revisions/run/logs/watch", nil)
	r.Host = "dev.example.test"
	r.AddCookie(&http.Cookie{Name: sourceCookie, Value: "production"})
	r.AddCookie(&http.Cookie{Name: accessCookie, Value: value})
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("stream did not start", response.StatusCode)
	}
	first := make([]byte, len("data: first\n\n"))
	if _, err := io.ReadFull(response.Body, first); err != nil || string(first) != "data: first\n\n" {
		t.Fatal("live stream did not flush")
	}
	_, _ = io.ReadAll(response.Body)
	if time.Now().Before(time.Unix(expiry, 0)) || time.Now().After(time.Unix(expiry, 0).Add(time.Second)) {
		t.Fatal("stream did not close at access expiry")
	}
}

package instancebridge

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNativeAuthenticationHeadersAndCookiesStayIsolated(t *testing.T) {
	f := connectFixture(t, true, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if OperationFromContext(r.Context()) != OperationNative {
			t.Error("wrong operation")
		}
		if _, ok := IdentityFromContext(r.Context()); ok {
			t.Error("cloud identity injected into native login")
		}
		for _, name := range []string{"Authorization", "Impersonate-User", "X-Doodship-Role", "X-Forwarded-Host"} {
			if r.Header.Get(name) != "" {
				t.Errorf("header forwarded: %s", name)
			}
		}
		if _, err := r.Cookie("__Host-dispatch-tenant"); err == nil {
			t.Error("cloud session forwarded")
		}
		if _, err := r.Cookie("tracking"); err == nil {
			t.Error("unrelated cookie forwarded")
		}
		if cookie, err := r.Cookie(NativeSessionCookie); err != nil || cookie.Value != "native-session" {
			t.Error("native session lost")
		}
		http.SetCookie(w, &http.Cookie{Name: NativeSessionCookie, Value: "new-session", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode})
		http.SetCookie(w, &http.Cookie{Name: NativeOAuthCookie, Value: "wrong-domain", Domain: "doodship.test", Path: "/", Secure: true, HttpOnly: true})
		http.SetCookie(w, &http.Cookie{Name: "__Host-dispatch-platform", Value: "forged-cloud", Path: "/", Secure: true, HttpOnly: true})
		w.WriteHeader(200)
	}), allow)
	r := httptest.NewRequest("GET", "https://agentops.doodship.test/api/v1/auth/providers", nil)
	r.Header.Set("Authorization", "Bearer static-owner")
	r.Header.Set("Impersonate-User", "other-user")
	r.Header.Set("X-Doodship-Role", "owner")
	r.Header.Set("X-Forwarded-Host", "other.doodship.test")
	r.AddCookie(&http.Cookie{Name: NativeSessionCookie, Value: "native-session"})
	r.AddCookie(&http.Cookie{Name: "__Host-dispatch-tenant", Value: "cloud-session"})
	r.AddCookie(&http.Cookie{Name: "tracking", Value: "unrelated"})
	w := httptest.NewRecorder()
	http.SetCookie(w, &http.Cookie{Name: "__Host-dispatch-tenant", Path: "/", Secure: true, HttpOnly: true, MaxAge: -1})
	f.b.ProxyOperation(w, r, "a", OperationNative, Identity{UserID: "cloud-owner", Role: "owner"})
	if w.Code != 200 {
		t.Fatalf("native request: %d %s", w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 || cookies[0].Name != "__Host-dispatch-tenant" || cookies[0].Value != "" || cookies[0].MaxAge != -1 || cookies[1].Name != NativeSessionCookie || cookies[1].SameSite != http.SameSiteLaxMode || cookies[1].Domain != "" {
		t.Fatalf("response cookie isolation lost: %#v", cookies)
	}
}

func TestNativeAuthenticationRoutesAreBounded(t *testing.T) {
	for _, test := range []struct {
		method, path string
		allowed      bool
	}{
		{"GET", "/api/v1/auth/providers", true}, {"POST", "/api/v1/auth/providers/github/start", true},
		{"GET", "/api/v1/auth/callback?code=one", true}, {"POST", "/api/v1/auth/exchange", true},
		{"POST", "/api/v1/auth/login", true}, {"POST", "/api/v1/auth/logout", true},
		{"POST", "/api/v1/auth/setup", false}, {"POST", "/api/v1/auth/providers", false},
		{"GET", "/api/v1/auth/providers/manifest/callback", false}, {"POST", "/api/v1/auth/providers/a/b/start", false},
		{"POST", "/api/v1/auth/providers/a%2Fb/start", false}, {"POST", "/api/v1/auth/../auth/login", false},
		{"GET", "/api/v1/overview", false}, {"POST", "/api/v1/instance-connection", false},
	} {
		r := httptest.NewRequest(test.method, "https://agentops.doodship.test"+test.path, nil)
		if NativeAuthenticationRequestAllowed(r) != test.allowed {
			t.Errorf("authentication scope %s %s", test.method, test.path)
		}
	}
	for _, p := range []string{"/api/v1/auth/setup", "/api/v1/auth/providers/manifest/callback", "/api/v1/instance-connection", "/api/v1/edge/nodes/a/session", "/api/v1/automation-accounts", "/api/v1/service-accounts", "/api/v1/bootstrap/claims/a"} {
		if NativeRequestAllowed(httptest.NewRequest("POST", "https://agentops.doodship.test"+p, nil)) {
			t.Errorf("management scope allowed: %s", p)
		}
	}
}

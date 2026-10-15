package instancebridge

import (
	"net/http"
	"path"
	"strings"
)

const NativeSessionCookie = "__Host-dispatch-instance-session"
const NativeOAuthCookie = "__Host-dispatch-oauth"
const nativeSignInHeader = "X-Doodship-Native-Sign-In"

func safeNativePath(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasPrefix(p, "/api/v1/") && path.Clean(p) == p && r.URL.EscapedPath() == p && !strings.Contains(p, "\\")
}

// NativeAuthenticationRequestAllowed exposes existing sign-in routes only.
// Setup, provider administration and credential creation are excluded.
func NativeAuthenticationRequestAllowed(r *http.Request) bool {
	if !safeNativePath(r) {
		return false
	}
	p := r.URL.Path
	if r.Method == http.MethodGet {
		return p == "/api/v1/auth/providers" || p == "/api/v1/auth/callback"
	}
	if r.Method != http.MethodPost {
		return false
	}
	if p == "/api/v1/auth/login" || p == "/api/v1/auth/discover" || p == "/api/v1/auth/exchange" || p == "/api/v1/auth/logout" {
		return true
	}
	id, ok := strings.CutPrefix(p, "/api/v1/auth/providers/")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, "/start")
	return ok && id != "" && !strings.ContainsAny(id, "/\\")
}

func NativeRequestAllowed(r *http.Request) bool {
	if NativeAuthenticationRequestAllowed(r) || AccessManagementRequestAllowed(r) {
		return true
	}
	if !safeNativePath(r) {
		return false
	}
	p := r.URL.Path
	for _, prefix := range []string{"/api/v1/instance-connection", "/api/v1/automation", "/api/v1/service-accounts", "/api/v1/edge/", "/api/v1/bootstrap/"} {
		if strings.HasPrefix(p, prefix) {
			return false
		}
	}
	if strings.HasPrefix(p, "/api/v1/auth/") {
		return r.Method == http.MethodGet && (p == "/api/v1/auth/me" || p == "/api/v1/auth/profile" || p == "/api/v1/auth/links") || r.Method == http.MethodPut && p == "/api/v1/auth/password"
	}
	if strings.HasPrefix(p, "/api/v1/users") || strings.HasPrefix(p, "/api/v1/teams") || strings.HasPrefix(p, "/api/v1/role-assignments") {
		return r.Method == http.MethodGet
	}
	return true
}

func nativeCookie(name string) bool { return name == NativeSessionCookie || name == NativeOAuthCookie }

func filterNativeCookies(h http.Header) {
	r := &http.Request{Header: h}
	cookies := r.Cookies()
	h.Del("Cookie")
	for _, cookie := range cookies {
		if nativeCookie(cookie.Name) {
			r.AddCookie(cookie)
		}
	}
	values := h.Values("Set-Cookie")
	h.Del("Set-Cookie")
	for _, value := range values {
		response := &http.Response{Header: http.Header{"Set-Cookie": []string{value}}}
		for _, cookie := range response.Cookies() {
			if nativeCookie(cookie.Name) && cookie.Domain == "" && cookie.Path == "/" && cookie.Secure && cookie.HttpOnly {
				cookie.SameSite = http.SameSiteLaxMode
				h.Add("Set-Cookie", cookie.String())
			}
		}
	}
}

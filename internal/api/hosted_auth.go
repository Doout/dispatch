package api

import (
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

// HostedAuth is supplied by the platform host router for one isolated tenant
// store. Authentication must recheck membership, including on every stream tick.
// No request header can install or change this callback.
type HostedAuth struct {
	TenantID     string
	LoginURL     string
	Authenticate func(*http.Request) (core.Identity, error)
}

func (a *API) hostedBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.auth.Hosted == nil {
			next.ServeHTTP(w, r)
			return
		}
		path := strings.TrimSuffix(r.URL.Path, "/")
		if path == "/api/v1/auth/status" {
			writeJSON(w, http.StatusOK, map[string]any{"setupRequired": false, "tokenLoginAvailable": false, "hosted": true, "loginUrl": a.auth.Hosted.LoginURL})
			return
		}
		// Global identity changes are served only by the platform account API.
		// In particular, tenant-local shadow users cannot mint login sessions.
		if strings.HasPrefix(path, "/api/v1/auth/") && path != "/api/v1/auth/me" && path != "/api/v1/auth/profile" ||
			strings.HasPrefix(path, "/api/v1/users") && r.Method != http.MethodGet {
			a.hostedIdentityRoute(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostedIdentityRoute prevents tenant-local account records from becoming a
// second login system. The platform handles global accounts and memberships.
func (a *API) hostedIdentityRoute(w http.ResponseWriter, r *http.Request) {
	problem(w, http.StatusForbidden, "Account managed centrally", "Manage your account and tenant membership from the main sign-in site.")
}

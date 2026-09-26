package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func TestRouteGroupsRequireAuthentication(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "owner-token"}, false)
	defer cleanup()
	public := map[string]bool{}
	for _, route := range []string{
		"GET /auth/status", "POST /auth/setup", "POST /auth/login",
		"GET /auth/providers", "POST /auth/discover", "POST /auth/providers/{id}/start",
		"GET /auth/callback", "GET /auth/providers/manifest/callback", "POST /auth/exchange",
		"POST /events/github", "POST /events/github/apps/{id}",
		"GET /github-apps/manifest/callback", "GET /laneway-applications/setup",
		"GET /laneway-networks/callback", "POST /edge/nodes/{id}/enroll",
		"POST /edge/nodes/{id}/challenge", "POST /edge/nodes/{id}/session",
		"GET /edge/nodes/{id}/jobs/next", "POST /edge/nodes/{id}/jobs/{jobId}/complete",
	} {
		public[route] = true
	}
	parameter := regexp.MustCompile(`\{[^}]+\}`)
	err := chi.Walk(handler.(*API).handler.(chi.Routes), func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(path, "/api/v1/") {
			return nil
		}
		path = strings.TrimSuffix(path, "/")
		if public[method+" "+strings.TrimPrefix(path, "/api/v1")] {
			return nil
		}
		t.Run(method+" "+path, func(t *testing.T) {
			request := httptest.NewRequest(method, parameter.ReplaceAllString(path, "missing"), strings.NewReader(`{}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated request: %d %s", response.Code, response.Body.String())
			}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRouteGroupsRejectMembersAndImpersonatedOwners(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "owner-token"}, false)
	defer cleanup()
	a := handler.(*API)
	now := time.Now().UTC()
	user := core.User{ID: "member", Username: "member", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	token, err := a.createSession(context.Background(), user.ID, identityForUser(user))
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"GET /secrets", "POST /secret-stores", "DELETE /private-networks/missing",
		"POST /laneway-networks/authorize", "POST /github-apps/manifest", "POST /servers",
		"POST /relay/ssh/install", "POST /auth/providers", "GET /users/missing/profile",
		"POST /config-sources", "POST /projects", "POST /preview-groups",
		"POST /workflow/preview-templates", "PUT /workflow/preview-templates/missing",
		"POST /workflow/temporary-resources/import", "DELETE /workflow/temporary-resources/missing",
		"PUT /workflow/preview-triggers/missing/url", "POST /workflow/revisions/missing/preview-report",
		"PUT /settings", "POST /users", "PUT /users/missing", "DELETE /users/missing",
		"POST /teams", "POST /role-assignments", "GET /access",
	} {
		for _, impersonating := range []bool{false, true} {
			name := "member "
			if impersonating {
				name = "impersonated member "
			}
			t.Run(name+route, func(t *testing.T) {
				method, path, _ := strings.Cut(route, " ")
				request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(`{}`))
				request.Header.Set("Authorization", "Bearer "+token)
				if impersonating {
					request.Header.Set("Authorization", "Bearer owner-token")
					request.Header.Set(impersonateUserHeader, user.ID)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != http.StatusForbidden {
					t.Fatalf("owner-only action: %d %s", response.Code, response.Body.String())
				}
			})
		}
	}
}

func TestPublicRoutesRemainAccessible(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "owner-token"}, false)
	defer cleanup()
	for _, path := range []string{"/healthz", "/api/v1/auth/status", "/api/v1/auth/providers"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s: %d %s", path, response.Code, response.Body.String())
		}
	}
}

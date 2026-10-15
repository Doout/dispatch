package instancebridge

import (
	"net/http/httptest"
	"testing"
)

func TestAccessManagementRoutesStayBounded(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{"POST", "/api/v1/users", true}, {"PUT", "/api/v1/users/member", true}, {"DELETE", "/api/v1/users/member", true},
		{"POST", "/api/v1/users/member/merge", true}, {"POST", "/api/v1/teams", true}, {"PUT", "/api/v1/teams/team", true},
		{"POST", "/api/v1/role-assignments", true}, {"DELETE", "/api/v1/role-assignments/grant", true},
		{"POST", "/api/v1/auth/providers", true}, {"PUT", "/api/v1/auth/providers/github", true}, {"DELETE", "/api/v1/auth/providers/github", true},
		{"POST", "/api/v1/auth/providers/github/verify", true}, {"POST", "/api/v1/auth/providers/manifest", true}, {"GET", "/api/v1/auth/providers/manifest/callback", true},
		{"POST", "/api/v1/auth/setup", false}, {"POST", "/api/v1/service-accounts", false}, {"PUT", "/api/v1/instance-connection", false},
		{"POST", "/api/v1/users/member", false}, {"POST", "/api/v1/users/member/credentials", false}, {"PUT", "/api/v1/users/member/extra", false},
		{"POST", "/api/v1/auth/providers/a/b/verify", false}, {"POST", "/api/v1/auth/providers/manifest/callback", false},
		{"POST", "/api/v1/users/../auth/setup", false}, {"PUT", "/api/v1/users/a%2Fb", false}, {"PUT", "/api/v1/users/a%5Cb", false},
	} {
		r := httptest.NewRequest(tc.method, "https://instance.example.test"+tc.path, nil)
		if got := AccessManagementRequestAllowed(r); got != tc.allowed {
			t.Errorf("%s %s: allowed=%v", tc.method, tc.path, got)
		}
	}
}

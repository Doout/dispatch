package instancebridge

import (
	"net/http"
	"strings"
)

// AccessManagementRequestAllowed forwards existing access administration routes.
// The installation still authenticates the user and requires its local owner role.
func AccessManagementRequestAllowed(r *http.Request) bool {
	if !safeNativePath(r) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	switch parts[0] {
	case "users", "teams", "role-assignments":
		if len(parts) == 1 {
			return r.Method == http.MethodPost
		}
		if len(parts) == 2 && parts[1] != "" {
			return r.Method == http.MethodDelete || r.Method == http.MethodPut && parts[0] != "role-assignments"
		}
		return parts[0] == "users" && len(parts) == 3 && parts[1] != "" && parts[2] == "merge" && r.Method == http.MethodPost
	case "auth":
		if len(parts) < 2 || parts[1] != "providers" {
			return false
		}
		if len(parts) == 2 {
			return r.Method == http.MethodPost
		}
		if len(parts) == 3 && parts[2] != "" {
			if parts[2] == "manifest" {
				return r.Method == http.MethodPost
			}
			return r.Method == http.MethodPut || r.Method == http.MethodDelete
		}
		return len(parts) == 4 && (parts[2] == "manifest" && parts[3] == "callback" && r.Method == http.MethodGet || parts[2] != "" && parts[3] == "verify" && r.Method == http.MethodPost)
	}
	return false
}

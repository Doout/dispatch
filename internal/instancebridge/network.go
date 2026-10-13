package instancebridge

import (
	"net/http"
	"strings"
)

// NetworkRequestAllowed identifies only routes which authenticate a node's own
// enrollment or session credential. It grants no access to human login routes.
func NetworkRequestAllowed(r *http.Request) bool {
	if r == nil || r.URL == nil || r.URL.EscapedPath() != r.URL.Path {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "edge" || parts[3] != "nodes" || !routeID(parts[4]) {
		return false
	}
	if len(parts) == 6 && r.Method == http.MethodPost {
		return parts[5] == "enroll" || parts[5] == "challenge" || parts[5] == "session"
	}
	if len(parts) == 7 && r.Method == http.MethodGet {
		return parts[5] == "jobs" && parts[6] == "next"
	}
	if len(parts) == 8 {
		if r.Method == http.MethodPost {
			return parts[5] == "jobs" && routeID(parts[6]) && parts[7] == "complete"
		}
		return r.Method == http.MethodGet && parts[5] == "runtime" && parts[6] == "jobs" && parts[7] == "next"
	}
	return len(parts) == 9 && r.Method == http.MethodPost && parts[5] == "runtime" && parts[6] == "jobs" && routeID(parts[7]) && (parts[8] == "heartbeat" || parts[8] == "complete")
}

func routeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

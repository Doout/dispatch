package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/analytics"
)

func (a *API) analyticsSummary(w http.ResponseWriter, r *http.Request) {
	days := 30
	if value := r.URL.Query().Get("days"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || (parsed != 7 && parsed != 30 && parsed != 90) {
			problem(w, http.StatusBadRequest, "Invalid range", "Choose 7, 30, or 90 days.")
			return
		}
		days = parsed
	}
	projects, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if project := strings.TrimSpace(r.URL.Query().Get("projectId")); project != "" {
		if !projects[project] {
			problem(w, http.StatusForbidden, "Access denied", "Project view permission is required.")
			return
		}
		projects = map[string]bool{project: true}
	}
	w.Header().Set("Cache-Control", "no-store")
	if a.eventConfig.Analytics == nil {
		writeJSON(w, http.StatusOK, analytics.EmptySummary("disabled", days))
		return
	}
	// A permission-filtered memory read, with no DuckDB work on the request path.
	writeJSON(w, http.StatusOK, a.eventConfig.Analytics.Summary(projects, days))
}

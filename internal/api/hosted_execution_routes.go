package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strings"
)

// Hosted controllers never install software or inspect a tenant's local paths.
// The ordinary self-hosted API keeps its existing behavior.
func (a *API) hostedExecutionBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.auth.Hosted != nil {
			path := strings.TrimSuffix(r.URL.Path, "/")
			blocked := strings.HasPrefix(path, "/api/v1/operations/backups") ||
				strings.HasPrefix(path, "/api/v1/relay/ssh/") || strings.HasPrefix(path, "/api/v1/builders/ssh/") ||
				strings.HasPrefix(path, "/api/v1/laneway-networks") || strings.HasPrefix(path, "/api/v1/laneway-applications") ||
				strings.HasPrefix(path, "/api/v1/private-networks/") && strings.HasSuffix(path, "/install-connector") ||
				strings.HasPrefix(path, "/api/v1/servers/") && strings.HasSuffix(path, "/repair") ||
				strings.HasPrefix(path, "/api/v1/infrastructure/bootstrap") && r.Method != http.MethodGet || path == "/api/v1/helm/inspect" ||
				strings.HasPrefix(path, "/api/v1/apps/") && (strings.HasSuffix(path, "/helm-values") || strings.HasSuffix(path, "/release-preview") || strings.HasSuffix(path, "/drift/check") || strings.HasSuffix(path, "/reapply")) ||
				strings.HasPrefix(path, "/api/v1/deployments/") && (strings.HasSuffix(path, "/diagnosis") || strings.HasSuffix(path, "/topology") || strings.HasSuffix(path, "/manifests") || strings.Contains(path, "/resources/"))
			if blocked {
				problem(w, http.StatusConflict, "Worker required", "This operation is unavailable on the hosted controller. Use an enrolled tenant worker or perform target setup on your own machine.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) hostedRollbackAllowed(w http.ResponseWriter, r *http.Request) bool {
	if a.auth.Hosted == nil {
		return true
	}
	deployment, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Deployment")
		return false
	}
	app, err := a.store.GetApp(r.Context(), deployment.AppID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return false
	}
	if app.BuildType == core.BuildTypeHelm || deployment.App != nil && deployment.App.BuildType == core.BuildTypeHelm {
		problem(w, http.StatusConflict, "Rollback unavailable", "Helm rollback requires a tenant worker operation. Deploy the desired chart revision instead.")
		return false
	}
	return true
}

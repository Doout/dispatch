package api

import (
	"net/http"

	"github.com/doout/dispatch/internal/ui"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func (a *API) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer, a.logRequest)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/relay/install.sh", a.relayInstallScript)
	r.Get("/relay/bin/{platform}", a.relayBinary)
	r.Get("/edge/install.sh", a.edgeInstallScript)
	r.Get("/edge/bin/{platform}", a.edgeBinary)
	r.Route("/api/v1", func(r chi.Router) {
		a.publicRoutes(r)
		r.Group(func(r chi.Router) {
			r.Use(a.authorize, a.auditMutation)
			a.identityRoutes(r)
			a.applicationsRoutes(r)
			a.deploymentsRoutes(r)
			a.workflowsRoutes(r)
			a.connectionsRoutes(r)
			a.previewsRoutes(r)
			a.projectsRoutes(r)
			a.operationsRoutes(r)
		})
	})
	r.Handle("/*", ui.Handler())
	r.Handle("/", ui.Handler())
	return r
}

// Webhooks, OAuth callbacks, and edge node requests verify their own credentials.
func (a *API) publicRoutes(r chi.Router) {
	r.Get("/auth/status", a.authStatus)
	r.Post("/auth/setup", a.setupAdmin)
	r.Post("/auth/login", a.login)
	r.Get("/auth/providers", a.publicAuthProviders)
	r.Post("/auth/discover", a.discoverAuth)
	r.Post("/auth/providers/{id}/start", a.startOAuth)
	r.Get("/auth/callback", a.completeOAuth)
	r.Get("/auth/providers/manifest/callback", a.completeAuthProviderManifest)
	r.Post("/auth/exchange", a.exchangeOAuthCode)
	r.Post("/events/github", a.githubWebhook)
	r.Post("/events/github/apps/{id}", a.githubAppWebhook)
	r.Get("/github-apps/manifest/callback", a.completeGitHubAppManifest)
	r.Get("/laneway-applications/setup", a.completeLanewayApplicationRegistration)
	r.Get("/laneway-networks/callback", a.completeLanewayAuthorization)
	r.Post("/edge/nodes/{id}/enroll", a.enrollEdgeNode)
	r.Post("/edge/nodes/{id}/challenge", a.challengeEdgeNode)
	r.Post("/edge/nodes/{id}/session", a.createEdgeSession)
	r.Get("/edge/nodes/{id}/jobs/next", a.leaseEdgeJob)
	r.Post("/edge/nodes/{id}/jobs/{jobId}/complete", a.completeEdgeJob)
}

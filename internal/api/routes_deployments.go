package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) deploymentsRoutes(r chi.Router) {
	r.Route("/deployments", func(r chi.Router) {
		r.Get("/", a.listDeployments)
		r.Route("/{id}", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(a.deploymentPermission(core.PermissionProjectView))
				r.Get("/identity", a.deploymentIdentity)
				r.Get("/compare-environment", a.compareEnvironments)
				r.Get("/compare", a.compareDeployments)
				r.Get("/", a.getDeployment)
				r.Get("/topology", a.getDeploymentTopology)
				r.Get("/manifests", a.getDeploymentManifests)
				r.Get("/resources/{kind}/{name}", a.getDeploymentResource)
				r.Get("/logs", a.getDeploymentLogs)
				r.Get("/events", a.deploymentEvents)
				r.Get("/release", a.getDeploymentRelease)
				r.Get("/diagnosis", a.diagnoseDeploymentRelease)
			})
			r.With(a.deploymentPermission(core.PermissionDeploymentCancel)).Post("/cancel", a.cancelDeployment)
			r.With(a.deploymentPermission(core.PermissionProjectConfigure)).Put("/release", a.updateDeploymentRelease)
			r.Group(func(r chi.Router) {
				r.Use(a.deploymentPermission(core.PermissionDeploymentRun))
				r.Post("/rollback-preview", a.previewDeploymentRollback)
				r.Post("/rollback", a.rollbackDeploymentRelease)
			})
		})
	})
	r.Get("/deployment-catalog", a.deploymentCatalog)
	r.Get("/deployment-search", a.deploymentSearch)
}

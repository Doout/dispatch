package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) previewsRoutes(r chi.Router) {
	r.Route("/event-triggers", func(r chi.Router) {
		r.Get("/", a.listEventTriggers)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(a.eventTriggerPermission(core.PermissionProjectConfigure))
			r.Put("/", a.updateEventTrigger)
			r.Delete("/", a.deleteEventTrigger)
		})
	})
	r.Get("/preview-environments", a.listPreviewEnvironments)
	r.Route("/preview-groups", func(r chi.Router) {
		r.Get("/", a.listPreviewGroups)
		r.With(a.ownerOnly).Post("/", a.createPreviewGroup)
		r.Route("/{id}", func(r chi.Router) {
			r.With(a.previewGroupPermission(core.PermissionProjectView)).Get("/", a.getPreviewGroup)
			r.Group(func(r chi.Router) {
				r.Use(a.ownerOnly)
				r.Put("/", a.updatePreviewGroup)
				r.Delete("/", a.deletePreviewGroup)
			})
		})
	})
	r.Route("/preview-group-runs", func(r chi.Router) {
		r.Get("/", a.listPreviewGroupRuns)
		r.Route("/{id}", func(r chi.Router) {
			r.With(a.previewGroupRunPermission(core.PermissionProjectView)).Get("/", a.getPreviewGroupRun)
			r.With(a.previewGroupRunPermission(core.PermissionDeploymentRun)).Post("/cleanup", a.cleanupPreviewGroupRun)
		})
	})
}

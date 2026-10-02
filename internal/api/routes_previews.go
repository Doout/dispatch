package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) previewsRoutes(r chi.Router) {
	r.Get("/events/rules", a.listEventRules)
	r.Get("/events/activity", a.listEventActivity)
	r.With(a.ownerOnly).Get("/events/deliveries", a.listWebhookDeliveries)
	r.Route("/event-triggers", func(r chi.Router) {
		r.Get("/", a.listEventTriggers)
		r.Route("/{id}", func(r chi.Router) {
			r.Use(a.eventTriggerPermission(core.PermissionProjectConfigure))
			r.Put("/", a.updateEventTrigger)
			a.destructiveRoute(r, "DELETE", "/", "event-trigger", "delete", a.deleteEventTrigger)
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
				a.destructiveRoute(r, "DELETE", "/", "preview-group", "delete", a.deletePreviewGroup)
			})
		})
	})
	r.Route("/preview-group-runs", func(r chi.Router) {
		r.Get("/", a.listPreviewGroupRuns)
		r.Route("/{id}", func(r chi.Router) {
			r.With(a.previewGroupRunPermission(core.PermissionProjectView)).Get("/", a.getPreviewGroupRun)
			a.destructiveRoute(r.With(a.previewGroupRunPermission(core.PermissionDeploymentRun)), "POST", "/cleanup", "preview-run", "cleanup", a.cleanupPreviewGroupRun)
		})
	})
}

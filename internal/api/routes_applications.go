package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) applicationsRoutes(r chi.Router) {
	r.Route("/apps", func(r chi.Router) {
		r.Get("/", a.listApps)
		r.Post("/", a.createApp)
		r.Route("/{id}", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(a.appPermission(core.PermissionProjectView))
				r.Get("/sync", a.getApplicationSync)
				r.Get("/route", a.getApplicationRoute)
				r.Get("/service-bindings", a.getAppServiceBindings)
				r.Get("/helm-values", a.getAppHelmValues)
				r.Get("/deployment-history", a.applicationDeploymentHistory)
				r.Get("/owner", a.getApplicationOwner)
				r.Get("/observations", a.getApplicationObservations)
				r.Get("/health-policy", a.getAppHealthPolicy)
				r.Get("/activity", a.applicationReleaseActivity)
			})
			r.Group(func(r chi.Router) {
				r.Use(a.appPermission(core.PermissionProjectConfigure))
				r.Post("/drift/check", a.checkApplicationDrift)
				r.Post("/route/check", a.checkApplicationRoute)
				r.With(a.idleAppMutation).Put("/service-bindings", a.updateAppServiceBindings)
				r.With(a.idleAppMutation).Put("/helm-values", a.updateAppHelmValues)
				r.With(a.idleAppMutation).Put("/hooks", a.updateAppHooks)
				r.With(a.idleAppMutation).Put("/health-policy", a.updateAppHealthPolicy)
				a.destructiveRoute(r, "DELETE", "/", "application", "delete", a.deleteApp)
				r.Post("/event-triggers", a.createEventTrigger)
				r.Put("/observations", a.updateApplicationObservations)
				r.Post("/observations/check", a.checkApplicationObservations)
			})
			r.Group(func(r chi.Router) {
				r.Use(a.appPermission(core.PermissionDeploymentRun))
				r.Post("/reapply", a.reapplyApplication)
				a.destructiveRoute(r, "POST", "/cleanup", "application", "cleanup", a.cleanupApp)
				r.Post("/deployments", a.startDeployment)
				r.Post("/release-preview", a.previewApplicationRelease)
			})
			r.With(a.requireOperationsEnabled, a.appPermission(core.PermissionProjectConfigure)).Put("/owner", a.setApplicationOwner)
		})
	})
	r.Post("/helm/inspect", a.inspectHelmSource)
	r.Route("/services", func(r chi.Router) {
		r.Get("/", a.listServices)
		r.With(a.directUserOnly).Post("/", a.createService)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", a.getService)
			a.destructiveRoute(r, "DELETE", "/", "service", "delete", a.deleteService)
			r.Post("/verify", a.verifyService)
			r.Get("/impact", a.serviceImpact)
			r.Post("/redeploy", a.redeployServiceConsumers)
			r.With(a.directUserOnly).Put("/", a.updateService)
		})
	})
	r.Route("/service-templates", func(r chi.Router) {
		r.Get("/", a.listServiceTemplates)
		r.With(a.directUserOnly).Post("/", a.createServiceTemplate)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", a.getServiceTemplate)
			r.With(a.directUserOnly).Put("/", a.updateServiceTemplate)
			a.destructiveRoute(r.With(a.directUserOnly), "DELETE", "/", "service-template", "delete", a.deleteServiceTemplate)
			r.With(a.directUserOnly).Post("/runs", a.startServiceProvision)
		})
	})
	r.Get("/service-provision-runs", a.listServiceProvisionRuns)
	r.Get("/service-provision-runs/{id}", a.getServiceProvisionRun)
}

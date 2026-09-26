package api

import (
	"github.com/go-chi/chi/v5"
)

func (a *API) operationsRoutes(r chi.Router) {
	r.Route("/settings", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.getControllerSettings)
		r.Put("/", a.saveControllerSettings)
	})
	r.With(a.requireOperationsEnabled).Get("/audit", a.listAudit)
	r.Route("/operations", func(r chi.Router) {
		r.Use(a.requireOperationsEnabled)
		r.Get("/summary", a.operationsSummary)
		r.Get("/ownership", a.operationsOwnership)
		r.Group(func(r chi.Router) {
			r.Use(a.ownerOnly)
			r.Get("/backups", a.listBackups)
			r.Post("/backups", a.createBackup)
			r.Post("/backups/{id}/verify", a.verifyBackup)
		})
	})
	r.Route("/identity-team-mappings", func(r chi.Router) {
		r.Use(a.requireOperationsEnabled, a.ownerOnly)
		r.Get("/", a.listTeamMappings)
		r.Post("/", a.saveTeamMapping)
		r.Route("/{id}", func(r chi.Router) {
			r.Delete("/", a.removeTeamMapping)
		})
	})
	r.Route("/overview", func(r chi.Router) {
		r.Get("/", a.overview)
		r.Get("/watch", a.watchOverview)
	})
	r.Get("/analytics", a.analyticsSummary)
	r.Route("/contracts", func(r chi.Router) {
		r.Get("/provider", a.providerContract)
		r.Get("/runtime", a.runtimeContract)
	})
}

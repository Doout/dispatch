package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func (a *API) projectsRoutes(r chi.Router) {
	r.Route("/projects", func(r chi.Router) {
		r.Get("/", a.listProjects)
		r.With(a.ownerOnly).Post("/", a.createProject)
		r.Route("/{id}", func(r chi.Router) {
			r.Group(func(r chi.Router) {
				r.Use(a.projectPermission(core.PermissionProjectManage))
				r.Put("/", a.updateProject)
				r.Delete("/", a.deleteProject)
			})
			r.Group(func(r chi.Router) {
				r.Use(a.requireOperationsEnabled)
				r.Get("/owner-candidates", a.ownerCandidates)
				r.Get("/retention", a.getRetention)
				r.Put("/retention", a.saveRetention)
				r.Post("/retention/preview", a.previewRetention)
				r.Post("/retention/apply", a.applyRetention)
			})
		})
	})
}

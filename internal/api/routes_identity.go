package api

import (
	"github.com/go-chi/chi/v5"
)

func (a *API) identityRoutes(r chi.Router) {
	r.Get("/auth/me", a.authMe)
	r.Post("/auth/logout", a.logout)
	r.Get("/auth/profile", a.accountProfile)
	r.Get("/auth/links", a.accountAuthLinks)
	r.Group(func(r chi.Router) {
		r.Use(a.directUserOnly)
		r.Put("/auth/password", a.changePassword)
		r.Post("/auth/providers/{id}/link", a.startOAuthLink)
		r.Delete("/auth/providers/{id}/link", a.unlinkAuthProvider)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/auth/providers", a.createAuthProvider)
		r.Post("/auth/providers/manifest", a.startAuthProviderManifest)
		r.Put("/auth/providers/{id}", a.updateAuthProvider)
		r.Post("/auth/providers/{id}/verify", a.verifyAuthProvider)
		r.Delete("/auth/providers/{id}", a.deleteAuthProvider)
	})
	r.Route("/users", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/", a.createUser)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateUser)
			r.Delete("/", a.deleteUser)
			r.Get("/profile", a.userProfile)
			r.Post("/merge", a.mergeUser)
		})
	})
	r.Route("/teams", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/", a.createTeam)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateTeam)
			r.Delete("/", a.deleteTeam)
		})
	})
	r.Route("/role-assignments", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/", a.upsertRoleAssignment)
		r.Route("/{id}", func(r chi.Router) {
			r.Delete("/", a.deleteRoleAssignment)
		})
	})
	r.With(a.ownerOnly).Get("/access", a.accessOverview)
}

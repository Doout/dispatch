package api

import (
	"github.com/go-chi/chi/v5"
)

func (a *API) identityRoutes(r chi.Router) {
	a.automationRoutes(r)
	r.Get("/auth/me", a.authMe)
	r.Post("/auth/logout", a.logout)
	r.Get("/auth/profile", a.accountProfile)
	r.Get("/auth/links", a.accountAuthLinks)
	r.Group(func(r chi.Router) {
		r.Use(a.directUserOnly)
		r.Put("/auth/password", a.changePassword)
		r.Post("/auth/providers/{id}/link", a.startOAuthLink)
		a.destructiveRoute(r, "DELETE", "/auth/providers/{id}/link", "auth-link", "delete", a.unlinkAuthProvider)
	})
	r.Group(func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/auth/providers", a.createAuthProvider)
		r.Post("/auth/providers/manifest", a.startAuthProviderManifest)
		r.Put("/auth/providers/{id}", a.updateAuthProvider)
		r.Post("/auth/providers/{id}/verify", a.verifyAuthProvider)
		a.destructiveRoute(r, "DELETE", "/auth/providers/{id}", "auth-provider", "delete", a.deleteAuthProvider)
	})
	r.Route("/users", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/", a.createUser)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateUser)
			a.destructiveRoute(r, "DELETE", "/", "user", "delete", a.deleteUser)
			r.Get("/profile", a.userProfile)
			r.Post("/merge", a.mergeUser)
		})
	})
	r.Route("/teams", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/", a.createTeam)
		r.Route("/{id}", func(r chi.Router) {
			r.Put("/", a.updateTeam)
			a.destructiveRoute(r, "DELETE", "/", "team", "delete", a.deleteTeam)
		})
	})
	r.Route("/role-assignments", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Post("/", a.upsertRoleAssignment)
		r.Route("/{id}", func(r chi.Router) {
			a.destructiveRoute(r, "DELETE", "/", "role-assignment", "delete", a.deleteRoleAssignment)
		})
	})
	r.With(a.ownerOnly).Get("/access", a.accessOverview)
}

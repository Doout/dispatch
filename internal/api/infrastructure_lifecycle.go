package api

import (
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

// The owner boundary is replaced by explicit project infrastructure grants when
// scoped automation identities are configured; provider administration stays owner-only.
func (a *API) infrastructureLifecycleRoutes(r chi.Router) {
	r.Route("/infrastructure/servers", func(r chi.Router) {
		r.Use(a.ownerOnly)
		r.Get("/", a.listManagedServers)
		r.Post("/review", a.reviewManagedServer)
		r.Post("/", a.createManagedServer)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/operations", a.managedServerOperations)
			r.Post("/enrollment", a.enrollManagedServer)
			r.Post("/adopt", a.adoptManagedServer)
			r.Post("/delete-review", a.reviewManagedServerDeletion)
			r.Post("/delete", a.deleteManagedServer)
		})
	})
	r.With(a.ownerOnly).Post("/infrastructure/operations/{id}/{action}", a.changeInfrastructureOperation)
	r.With(a.ownerOnly).Get("/projects/{id}/infrastructure/providers", a.projectInfrastructureCatalog)
	r.With(a.ownerOnly).Post("/projects/{id}/infrastructure/providers/{providerId}/options", a.projectInfrastructureOptions)
}
func (a *API) lifecycleManager(w http.ResponseWriter) *provision.Manager {
	m := a.infrastructureManager()
	if m == nil {
		problem(w, 503, "Infrastructure unavailable", "Durable provider storage is required.")
	}
	return m
}
func (a *API) listManagedServers(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	items, err := m.ListManaged(r.Context())
	a.list(w, items, err)
}
func (a *API) reviewManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.CreateInput
	if !decode(w, r, &in) {
		return
	}
	in.ActorID = currentIdentity(r.Context()).ID
	item, err := m.ReviewCreate(r.Context(), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, item)
}
func (a *API) createManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Acceptance
	if !decode(w, r, &in) {
		return
	}
	item, err := m.AcceptCreate(r.Context(), currentIdentity(r.Context()).ID, in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 202, item)
}
func (a *API) managedServerOperations(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	items, err := m.Operations(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) changeInfrastructureOperation(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	if err := m.ChangeOperation(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "action")); err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.WriteHeader(204)
}
func (a *API) enrollManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	item, err := m.Enrollment(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, item)
}
func (a *API) adoptManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Adoption
	if !decode(w, r, &in) {
		return
	}
	item, err := m.Adopt(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) reviewManagedServerDeletion(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetServer(r.Context(), id); err == nil {
		if err = a.refreshStorageReview(r.Context(), "server", id); err != nil {
			a.infrastructureProblem(w, err)
			return
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		a.infrastructureProblem(w, err)
		return
	}
	item, err := m.ReviewDelete(r.Context(), id)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, item)
}
func (a *API) deleteManagedServer(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Acceptance
	if !decode(w, r, &in) {
		return
	}
	id := chi.URLParam(r, "id")
	var item provision.Accepted
	remove := func() error {
		var err error
		item, err = m.Delete(r.Context(), id, currentIdentity(r.Context()).ID, in)
		return err
	}
	var err error
	if server, e := a.store.GetServer(r.Context(), id); e == nil {
		err = a.deploy.Storage.WithTarget(r.Context(), id, func() error {
			if e := a.deploy.Storage.RefreshLocked(r.Context(), server); e != nil {
				return e
			}
			return remove()
		})
	} else if !errors.Is(e, store.ErrNotFound) {
		err = e
	} else {
		err = remove()
	}
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 202, item)
}
func (a *API) projectInfrastructureCatalog(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	items, err := m.Catalog(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) projectInfrastructureOptions(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.ProjectOptionInput
	if !decode(w, r, &in) {
		return
	}
	items, err := m.ProjectOptions(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "providerId"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, items)
}

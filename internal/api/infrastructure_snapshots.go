package api

import (
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provision"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) infrastructureSnapshotRoutes(r chi.Router) {
	r.Get("/projects/{id}/infrastructure/snapshots", a.listInfrastructureSnapshots)
	r.Route("/infrastructure/snapshots", func(r chi.Router) {
		r.Post("/accept", a.acceptInfrastructureSnapshot)
		r.Get("/{id}", a.getInfrastructureSnapshot)
		r.Post("/{id}/delete-review", a.reviewInfrastructureSnapshotDelete)
		r.Post("/{id}/resolve", a.resolveInfrastructureSnapshot)
	})
}
func (a *API) listInfrastructureSnapshots(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	project := chi.URLParam(r, "id")
	if !a.requireProject(w, r, core.PermissionInfrastructureInspect, project) {
		return
	}
	items, err := m.ListSnapshots(r.Context(), project)
	a.list(w, items, err)
}
func (a *API) getInfrastructureSnapshot(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	data, ok := a.store.(store.InfrastructureSnapshotStore)
	if !ok {
		problem(w, 503, "Snapshots unavailable", "Durable snapshot storage is required.")
		return
	}
	snapshot, err := data.GetInfrastructureSnapshot(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	if err = a.authorizeInfrastructure(r.Context(), snapshot.ProjectID, snapshot.ProviderID, "infrastructure.inspect"); err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, snapshot)
}
func (a *API) reviewInfrastructureSnapshot(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.SnapshotInput
	if !decode(w, r, &in) {
		return
	}
	review, err := m.ReviewSnapshot(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 201, review)
}
func (a *API) reviewInfrastructureSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	review, err := m.ReviewSnapshotDelete(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 201, review)
}
func (a *API) acceptInfrastructureSnapshot(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	data, ok := a.store.(store.InfrastructureSnapshotStore)
	if !ok {
		problem(w, 503, "Snapshots unavailable", "Durable snapshot storage is required.")
		return
	}
	var in provision.Acceptance
	if !decode(w, r, &in) {
		return
	}
	review, err := data.GetInfrastructureSnapshotReview(r.Context(), in.ReviewID)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	permission := core.PermissionSnapshotCreate
	if review.Action == "snapshot.delete" {
		permission = core.PermissionInfrastructureDelete
	}
	if !a.authorizeInfrastructureMutationReceipt(w, r, review.ProjectID, review.ProviderID, permission) {
		return
	}
	r, receipt, proceed := a.reserveMutation(w, r, review.ProjectID, review.Action, in, "infrastructure_operation", review.ServerID)
	if !proceed {
		return
	}
	identity := currentIdentity(r.Context())
	result, err := m.AcceptSnapshot(r.Context(), identity.Kind+":"+identity.ID, in)
	if err != nil {
		a.rejectInfrastructureMutation(w, r, err)
		return
	}
	if a.completeInfrastructureMutation(w, r, receipt, result.Operation.ID) {
		return
	}
	writeJSON(w, 202, result)
}

func (a *API) resolveInfrastructureSnapshot(w http.ResponseWriter, r *http.Request) {
	m := a.lifecycleManager(w)
	if m == nil {
		return
	}
	var in provision.Adoption
	if !decode(w, r, &in) {
		return
	}
	snapshot, err := m.ResolveSnapshot(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		a.infrastructureProblem(w, err)
		return
	}
	writeJSON(w, 200, snapshot)
}

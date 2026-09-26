package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/observe"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) RunObservations(ctx context.Context) {
	if a.observations != nil {
		a.observations.Run(ctx)
	}
}
func (a *API) getApplicationObservations(w http.ResponseWriter, r *http.Request) {
	if a.observations == nil {
		problem(w, http.StatusServiceUnavailable, "Observations unavailable", "Observation checks are not configured.")
		return
	}
	status, err := a.observations.Status(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
func (a *API) updateApplicationObservations(w http.ResponseWriter, r *http.Request) {
	var input observe.ConfigInput
	if !decode(w, r, &input) {
		return
	}
	if a.observations == nil {
		problem(w, http.StatusServiceUnavailable, "Observations unavailable", "Observation checks are not configured.")
		return
	}
	_, err := a.observations.Configure(r.Context(), chi.URLParam(r, "id"), currentIdentity(r.Context()).ID, input)
	if errors.Is(err, store.ErrObservationConflict) {
		problem(w, http.StatusConflict, "Settings changed", "Refresh the observation settings before saving.")
		return
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid observation settings", err.Error())
		return
	}
	a.getApplicationObservations(w, r)
}
func (a *API) checkApplicationObservations(w http.ResponseWriter, r *http.Request) {
	if a.observations == nil {
		problem(w, http.StatusServiceUnavailable, "Observations unavailable", "Observation checks are not configured.")
		return
	}
	_, err := a.observations.Check(r.Context(), chi.URLParam(r, "id"), "manual")
	if errors.Is(err, observe.ErrBusy) {
		problem(w, http.StatusConflict, "Application busy", "Wait for the deployment or observation to finish.")
		return
	}
	if errors.Is(err, store.ErrObservationConflict) {
		problem(w, http.StatusConflict, "Settings changed", "Observation settings changed during the check. Run it again.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	a.getApplicationObservations(w, r)
}

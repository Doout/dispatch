package api

import (
	"net/http"

	"github.com/doout/dispatch/internal/instanceconnection"
)

func (a *API) instanceConnectionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a.eventConfig.InstanceConnection.Status())
}
func (a *API) configureInstanceConnection(w http.ResponseWriter, r *http.Request) {
	var input instanceconnection.Setup
	if !decode(w, r, &input) {
		return
	}
	status, err := a.eventConfig.InstanceConnection.Configure(r.Context(), input)
	if err != nil {
		problem(w, http.StatusBadRequest, "Connection not saved", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, status)
}
func (a *API) disconnectInstanceConnection(w http.ResponseWriter, r *http.Request) {
	if err := a.eventConfig.InstanceConnection.Disconnect(r.Context()); err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

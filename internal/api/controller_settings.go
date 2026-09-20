package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/doout/dispatch/internal/core"
)

func (a *API) getControllerSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.GetControllerSettings(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) saveControllerSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		OperationsEnabled *bool `json:"operationsEnabled"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		problem(w, http.StatusBadRequest, "Invalid settings", "Send valid JSON containing only operationsEnabled as a boolean.")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, http.StatusBadRequest, "Invalid settings", "Send a single JSON settings object.")
		return
	}
	if input.OperationsEnabled == nil {
		problem(w, http.StatusBadRequest, "Invalid settings", "Supply operationsEnabled as a boolean.")
		return
	}
	settings := core.ControllerSettings{OperationsEnabled: *input.OperationsEnabled}
	if err := a.store.SaveControllerSettings(r.Context(), settings); err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, settings)
}

func (a *API) requireOperationsEnabled(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		settings, err := a.store.GetControllerSettings(r.Context())
		if err != nil {
			a.internal(w, err)
			return
		}
		if !settings.OperationsEnabled {
			problem(w, http.StatusForbidden, "Operations disabled", "A controller owner can enable Operations in Settings.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

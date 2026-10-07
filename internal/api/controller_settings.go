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
		OperationsEnabled json.RawMessage `json:"operationsEnabled"`
		UIFeatures        json.RawMessage `json:"uiFeatures"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		problem(w, http.StatusBadRequest, "Invalid settings", "Send a JSON object containing operationsEnabled or uiFeatures.")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		problem(w, http.StatusBadRequest, "Invalid settings", "Send a single JSON settings object.")
		return
	}
	var patch core.ControllerSettingsPatch
	if input.OperationsEnabled != nil {
		if err := json.Unmarshal(input.OperationsEnabled, &patch.OperationsEnabled); err != nil || patch.OperationsEnabled == nil {
			problem(w, http.StatusBadRequest, "Invalid settings", "Supply operationsEnabled as a boolean.")
			return
		}
	}
	if input.UIFeatures != nil {
		var features map[string]*bool
		if err := json.Unmarshal(input.UIFeatures, &features); err != nil || len(features) == 0 {
			problem(w, http.StatusBadRequest, "Invalid settings", "Supply uiFeatures as an object containing at least one feature boolean.")
			return
		}
		patch.UIFeatures = make(map[string]bool, len(features))
		for name, enabled := range features {
			if !core.IsUIFeature(name) || enabled == nil {
				problem(w, http.StatusBadRequest, "Invalid settings", "Each uiFeatures entry must name a supported feature and contain a boolean.")
				return
			}
			patch.UIFeatures[name] = *enabled
		}
	}
	if patch.OperationsEnabled == nil && len(patch.UIFeatures) == 0 {
		problem(w, http.StatusBadRequest, "Invalid settings", "Supply at least one setting to change.")
		return
	}
	settings, err := a.store.UpdateControllerSettings(r.Context(), patch)
	if err != nil {
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

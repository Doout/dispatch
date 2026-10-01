package api

import (
	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (a *API) getAppHealthPolicy(w http.ResponseWriter, r *http.Request) {
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	policy, err := core.NormalizeHealthPolicy(app.HealthPolicy)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, policy)
}
func (a *API) updateAppHealthPolicy(w http.ResponseWriter, r *http.Request) {
	app, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	var policy core.HealthPolicy
	if !decode(w, r, &policy) {
		return
	}
	policy, err = core.NormalizeHealthPolicy(policy)
	if err != nil {
		problem(w, 422, "Invalid health policy", err.Error())
		return
	}
	for _, check := range policy.Checks {
		if check.Scope != "workload" && app.Domain == "" {
			problem(w, 422, "Route required", "Configure an application domain before requiring route or certificate readiness.")
			return
		}
	}
	app.HealthPolicy = policy
	if err = a.store.UpdateApp(r.Context(), app); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, policy)
}

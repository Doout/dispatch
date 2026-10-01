package api

import (
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (a *API) getApplicationRoute(w http.ResponseWriter, r *http.Request) {
	data, ok := a.store.(store.ApplicationRouteStore)
	if !ok {
		writeJSON(w, 200, nil)
		return
	}
	route, err := data.GetApplicationRoute(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, 200, nil)
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, route)
}
func (a *API) checkApplicationRoute(w http.ResponseWriter, r *http.Request) {
	data, ok := a.store.(store.ApplicationRouteStore)
	if !ok {
		problem(w, 503, "Routing unavailable", "Managed route storage is unavailable.")
		return
	}
	route, err := data.GetApplicationRoute(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application route")
		return
	}
	checked := routing.Probe{}.Check(r.Context(), route)
	if err = data.SaveApplicationRoute(r.Context(), checked); err != nil {
		problem(w, 409, "Route changed", "A deployment changed the route while its public endpoint was checked. Refresh and retry.")
		return
	}
	writeJSON(w, 200, checked)
}
func (a *API) updateServerRouting(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if server.Runtime != core.ServerRuntimeDocker {
		problem(w, 422, "Managed routing unavailable", "Use chart-managed ingress for Kubernetes targets. Managed Traefik routes apply to Docker targets.")
		return
	}
	var input struct {
		Routing *core.RoutingConfig `json:"routing"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err = routing.ValidateConfig(input.Routing); err != nil {
		problem(w, 422, "Routing configuration invalid", err.Error())
		return
	}
	if input.Routing == nil {
		if data, ok := a.store.(store.ApplicationRouteStore); ok {
			routes, err := data.ListApplicationRoutes(r.Context())
			if err != nil {
				a.internal(w, err)
				return
			}
			for _, route := range routes {
				if route.ServerID == server.ID {
					problem(w, 409, "Managed routes remain", "Clean up this target's application routes before disabling managed routing.")
					return
				}
			}
		}
	}
	server.Routing = input.Routing
	if err = a.store.UpdateServer(r.Context(), server); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, server)
}

package api

import (
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) providerContract(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": provider.APIVersion, "transport": "HTTP/JSON sidecar", "operations": []string{"manifest", "validate", "options", "createServer", "operation", "server", "deleteServer"}})
}

func (a *API) runtimeContract(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"apiVersion": runtimecontract.APIVersion,
		"enabled":    []string{core.ServerRuntimeDocker, core.ServerRuntimeKubernetes, core.ServerRuntimeOpenShift},
		"planned":    []string{}, "operations": runtimecontract.Operations(),
		"capabilitiesURL": "/api/v1/servers/{id}/capabilities",
		"semantics": map[string]string{
			"capabilities": "Query the configured executor for a target and build type before requesting an operation.",
			"source":       "Live repository deployments resolve the accepted commit before execution.",
			"recovery":     "Inspect uncertain outcomes before retrying runtime mutations.",
		},
	})
}

func (a *API) serverRuntimeCapabilities(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Server not found", "The requested target does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	buildType := core.BuildType(r.URL.Query().Get("buildType"))
	if buildType == "" {
		buildType = core.BuildTypeDockerfile
		if core.IsKubernetesRuntime(server.Runtime) {
			buildType = core.BuildTypeHelm
		}
	}
	if buildType != core.BuildTypeDockerfile && buildType != core.BuildTypeCompose && buildType != core.BuildTypeHelm {
		problem(w, http.StatusUnprocessableEntity, "Unknown build type", "Use dockerfile, compose or helm.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, a.deploy.RuntimeCapabilities(core.App{BuildType: buildType}, server))
}

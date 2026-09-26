package api

import (
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
)

func (a *API) providerContract(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": provider.APIVersion, "transport": "HTTP/JSON sidecar", "operations": []string{"manifest", "validate", "options", "createServer", "operation", "server", "deleteServer"}})
}

func (a *API) runtimeContract(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": "dispatch.runtime/v1", "enabled": []string{core.ServerRuntimeDocker, core.ServerRuntimeKubernetes, core.ServerRuntimeOpenShift}, "planned": []string{}, "operations": []string{"deploy", "inspect", "logs", "start", "stop", "rollback", "destroy"}})
}

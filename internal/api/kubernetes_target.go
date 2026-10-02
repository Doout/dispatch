package api

import (
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
)

func (a *API) inspectKubernetesTarget(w http.ResponseWriter, r *http.Request, config, previous *core.KubernetesServerConfig) bool {
	inspect := a.kubernetesTargetValidator
	if inspect == nil {
		inspect = kubeconfig.InspectTarget
	}
	evidence, err := inspect(r.Context(), *config)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "Kubernetes target unavailable", err.Error())
		return false
	}
	if previous != nil && previous.Validation != nil && (previous.Validation.ClusterUID != evidence.ClusterUID || previous.Validation.NamespaceUID != evidence.NamespaceUID) {
		problem(w, http.StatusConflict, "Kubernetes target changed", "Create a separate target for a different cluster or namespace. Existing deployment history must keep its original target.")
		return false
	}
	config.Validation = &evidence
	return true
}

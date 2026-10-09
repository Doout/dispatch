package api

import (
	"context"
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
)

type targetInspectionContextKey struct{}

type targetInspectionContext struct {
	request   *http.Request
	projectID string
}

func (a *API) inspectKubernetesTarget(w http.ResponseWriter, r *http.Request, config, previous *core.KubernetesServerConfig, projectIDs ...string) bool {
	inspect := a.kubernetesTargetValidator
	if inspect == nil {
		inspect = kubeconfig.InspectTarget
	}
	projectID := ""
	if len(projectIDs) > 0 {
		projectID = projectIDs[0]
	}
	ctx := context.WithValue(r.Context(), targetInspectionContextKey{}, targetInspectionContext{request: r, projectID: projectID})
	evidence, err := inspect(ctx, *config)
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

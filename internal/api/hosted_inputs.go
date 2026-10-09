package api

import (
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func (a *API) hostedServerInput(w http.ResponseWriter, runtime, node string, kubernetes *kubernetesServerRequest) bool {
	if runtime == core.ServerRuntimeDocker && node == "" {
		problem(w, http.StatusUnprocessableEntity, "Worker required", "Enroll a tenant worker before adding a Docker deployment target.")
		return false
	}
	if runtime == core.ServerRuntimeOpenShift {
		problem(w, http.StatusUnprocessableEntity, "Kubeconfig required", "Add this cluster as a Kubernetes target using an embedded kubeconfig. Run oc login on your own machine.")
		return false
	}
	if runtime == core.ServerRuntimeKubernetes && kubernetes != nil {
		if kubernetes.KubeconfigPath != "" || strings.EqualFold(strings.TrimSpace(kubernetes.Source), "path") || kubernetes.LoginCommand != "" {
			problem(w, http.StatusUnprocessableEntity, "Embedded credentials required", "Paste a kubeconfig with embedded credentials. Controller file paths and login commands are unavailable in hosted tenants.")
			return false
		}
		kubernetes.Source = "stored"
	}
	return true
}

func (a *API) hostedSourceInput(w http.ResponseWriter, repository string) bool {
	if a.auth.Hosted == nil || repository == "" {
		return true
	}
	if !hostedRepositoryURL(repository) {
		problem(w, http.StatusUnprocessableEntity, "Remote repository required", "Use an HTTPS or SSH repository URL. Local paths and Git helper protocols are unavailable in hosted tenants.")
		return false
	}
	return true
}

func hostedRepositoryURL(raw string) bool {
	return workflowrunner.ValidRepositoryURL(raw)
}

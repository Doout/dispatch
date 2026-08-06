package api

import (
	"github.com/doout/dispatch/internal/kubeconfig"
)

func validateKubeconfig(path, requestedContext string) (string, error) {
	return kubeconfig.ValidatePath(path, requestedContext)
}

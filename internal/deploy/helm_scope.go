package deploy

import (
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/cli"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/rest"
)

func selectHelmCredentials(settings *cli.EnvSettings, server core.Server) {
	settings.KubeConfig, settings.KubeContext = server.Kubernetes.KubeconfigPath, server.Kubernetes.Context
	// A target's credentials must not inherit HELM_KUBE* overrides from the
	// controller process, including impersonation and TLS bypass settings.
	settings.KubeToken, settings.KubeAPIServer, settings.KubeCaFile, settings.KubeTLSServerName = "", "", "", ""
	settings.KubeAsUser, settings.KubeAsGroups = "", nil
	settings.KubeInsecureSkipTLSVerify = false
}

type namespaceRESTGetter struct {
	genericclioptions.RESTClientGetter
	namespace string
}

func scopedHelmGetter(getter genericclioptions.RESTClientGetter, server core.Server) genericclioptions.RESTClientGetter {
	if server.Kubernetes != nil && server.Kubernetes.Validation != nil {
		return namespaceRESTGetter{RESTClientGetter: getter, namespace: server.Kubernetes.Namespace}
	}
	return getter
}

func (g namespaceRESTGetter) ToRESTConfig() (*rest.Config, error) {
	config, err := g.RESTClientGetter.ToRESTConfig()
	if err != nil {
		return nil, err
	}
	config = rest.CopyConfig(config)
	previous := config.WrapTransport
	config.WrapTransport = func(next http.RoundTripper) http.RoundTripper {
		if previous != nil {
			next = previous(next)
		}
		return namespaceTransport{next: next, namespace: g.namespace}
	}
	return config, nil
}

type namespaceTransport struct {
	next      http.RoundTripper
	namespace string
}

func (t namespaceTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !allowedNamespaceRequest(request, t.namespace) {
		return nil, errors.New("Helm access outside the registered namespace is blocked")
	}
	return t.next.RoundTrip(request)
}

func allowedNamespaceRequest(request *http.Request, namespace string) bool {
	value := strings.TrimSuffix(request.URL.Path, "/")
	if namespace == "" || path.Clean(value) != value {
		return false
	}
	if request.Method == http.MethodGet && (value == "/version" || value == "/api" || value == "/apis" || value == "/openapi/v2" || value == "/openapi/v3" || strings.HasPrefix(value, "/openapi/v3/")) {
		return true
	}
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	for _, part := range parts {
		if part == "proxy" {
			return false
		}
	}
	var offset int
	switch {
	case len(parts) >= 2 && parts[0] == "api":
		offset = 2
	case len(parts) >= 3 && parts[0] == "apis":
		offset = 3
	default:
		return false
	}
	// API discovery can describe all resources, but never reads resource data.
	if len(parts) == offset {
		return request.Method == http.MethodGet
	}
	if len(parts) < offset+2 || parts[offset] != "namespaces" || parts[offset+1] != namespace {
		return false
	}
	if len(parts) == offset+2 {
		return request.Method == http.MethodGet
	}
	return true
}

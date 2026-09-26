package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/openshift"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListServers(r.Context())
	a.list(w, items, err)
}

type createServerRequest struct {
	Name       string                   `json:"name"`
	Address    string                   `json:"address"`
	Runtime    string                   `json:"runtime"`
	AgentMode  string                   `json:"agentMode"`
	Kubernetes *kubernetesServerRequest `json:"kubernetes"`
	Relay      *relayServerRequest      `json:"relay"`
}

type relayServerRequest struct {
	AccessToken *string `json:"accessToken"`
}

type kubernetesServerRequest struct {
	Source               string  `json:"source"`
	KubeconfigPath       string  `json:"kubeconfigPath"`
	Kubeconfig           *string `json:"kubeconfig"`
	CertificateAuthority *string `json:"certificateAuthority"`
	Context              string  `json:"context"`
	Namespace            string  `json:"namespace"`
	LoginCommand         string  `json:"loginCommand"`
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	var input createServerRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Address = strings.TrimSpace(input.Name), strings.TrimSpace(input.Address)
	if input.Name == "" {
		problem(w, http.StatusBadRequest, "Server name required", "Enter a server name before saving it.")
		return
	}
	input.Runtime = normalizeServerRuntime(input.Runtime)
	item := core.Server{ID: ulid.Make().String(), Name: input.Name, Runtime: input.Runtime, CreatedAt: time.Now().UTC()}
	switch input.Runtime {
	case core.ServerRuntimeDocker:
		if input.Address == "" {
			problem(w, http.StatusBadRequest, "Server address required", "Enter the Docker host name or IP address.")
			return
		}
		if strings.EqualFold(input.Address, "local") {
			problem(w, http.StatusConflict, "Controller Docker is managed automatically", "Mount the Docker socket to register this controller.")
			return
		}
		item.Address, item.State, item.AgentMode = input.Address, "pending", "ssh-bootstrap"
	case core.ServerRuntimeKubernetes:
		kubernetes, detail := validateKubernetesServer(input.Kubernetes, nil)
		if detail != "" {
			problem(w, http.StatusBadRequest, "Kubernetes connection required", detail)
			return
		}
		item.Address, item.State, item.AgentMode, item.Kubernetes = kubernetesAddress(kubernetes), "ready", "direct", kubernetes
	case core.ServerRuntimeOpenShift:
		if input.Kubernetes == nil {
			problem(w, http.StatusBadRequest, "OpenShift login required", "Paste a non-interactive oc login command.")
			return
		}
		result, err := a.openShift.Bootstrap(r.Context(), input.Kubernetes.LoginCommand)
		if err != nil {
			a.openShiftProblem(w, err)
			return
		}
		namespace := strings.TrimSpace(input.Kubernetes.Namespace)
		if namespace == "" {
			namespace = "default"
		}
		kubernetes := openshift.Config(result, namespace)
		item.Address, item.State, item.AgentMode, item.Kubernetes = result.Server, "ready", "direct", &kubernetes
	case core.ServerRuntimeRelay:
		address, detail := validateRelayAddress(input.Address)
		if detail != "" {
			problem(w, http.StatusBadRequest, "Relay connection invalid", detail)
			return
		}
		if input.Relay == nil || input.Relay.AccessToken == nil || len(strings.TrimSpace(*input.Relay.AccessToken)) < 24 {
			problem(w, http.StatusBadRequest, "Relay access token required", "Enter the access token configured on the relay node.")
			return
		}
		if a.eventConfig.Vault == nil {
			problem(w, http.StatusServiceUnavailable, "Encrypted storage required", "Configure the master key before adding a relay server.")
			return
		}
		encrypted, err := a.eventConfig.Vault.Encrypt("relay-server:"+item.ID+":access-token", []byte(strings.TrimSpace(*input.Relay.AccessToken)))
		if err != nil {
			a.internal(w, err)
			return
		}
		item.Address, item.State, item.AgentMode, item.Relay = address, "connecting", "outbound", &core.RelayServerConfig{EncryptedAccessToken: encrypted, AccessTokenConfigured: true}
	default:
		problem(w, http.StatusBadRequest, "Runtime unavailable", "Use docker, kubernetes, openshift, or relay.")
		return
	}
	if err := a.store.CreateServer(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type updateServerRequest struct {
	Name       string                   `json:"name"`
	Address    string                   `json:"address"`
	Kubernetes *kubernetesServerRequest `json:"kubernetes"`
	Relay      *relayServerRequest      `json:"relay"`
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if strings.EqualFold(item.Address, "local") {
		problem(w, http.StatusConflict, "Managed server cannot be changed", "The controller Docker server is reconciled automatically from its socket.")
		return
	}
	var input updateServerRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Address = strings.TrimSpace(input.Name), strings.TrimSpace(input.Address)
	if input.Name == "" {
		problem(w, http.StatusBadRequest, "Server name required", "Enter a server name before saving it.")
		return
	}
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, server := range servers {
		if server.ID != item.ID && strings.EqualFold(server.Name, input.Name) {
			problem(w, http.StatusConflict, "Server name already used", "Choose another server name.")
			return
		}
	}
	switch item.Runtime {
	case core.ServerRuntimeDocker:
		if input.Address == "" {
			problem(w, http.StatusBadRequest, "Server address required", "Enter the Docker host name or IP address.")
			return
		}
		if strings.EqualFold(input.Address, "local") {
			problem(w, http.StatusConflict, "Connection type cannot be changed", "Create a separate server when you need a different connection type.")
			return
		}
		item.Address = input.Address
	case core.ServerRuntimeKubernetes:
		kubernetes, detail := validateKubernetesServer(input.Kubernetes, item.Kubernetes)
		if detail != "" {
			problem(w, http.StatusBadRequest, "Kubernetes connection required", detail)
			return
		}
		item.Address, item.Kubernetes = kubernetesAddress(kubernetes), kubernetes
	case core.ServerRuntimeOpenShift:
		kubernetes, detail := validateKubernetesServer(input.Kubernetes, item.Kubernetes)
		if detail != "" {
			problem(w, http.StatusBadRequest, "OpenShift connection required", detail)
			return
		}
		item.Kubernetes = kubernetes
	case core.ServerRuntimeRelay:
		address, detail := validateRelayAddress(input.Address)
		if detail != "" {
			problem(w, http.StatusBadRequest, "Relay connection invalid", detail)
			return
		}
		item.Address = address
		if input.Relay != nil && input.Relay.AccessToken != nil && strings.TrimSpace(*input.Relay.AccessToken) != "" {
			if len(strings.TrimSpace(*input.Relay.AccessToken)) < 24 {
				problem(w, http.StatusBadRequest, "Relay access token invalid", "Use at least 24 characters.")
				return
			}
			encrypted, encryptErr := a.eventConfig.Vault.Encrypt("relay-server:"+item.ID+":access-token", []byte(strings.TrimSpace(*input.Relay.AccessToken)))
			if encryptErr != nil {
				a.internal(w, encryptErr)
				return
			}
			item.Relay.EncryptedAccessToken, item.Relay.AccessTokenConfigured = encrypted, true
		}
		item.State = "connecting"
	default:
		problem(w, http.StatusConflict, "Runtime unavailable", "This server uses an unsupported runtime.")
		return
	}
	item.Name = input.Name
	if err := a.store.UpdateServer(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func normalizeServerRuntime(runtime string) string {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "", core.ServerRuntimeDocker:
		return core.ServerRuntimeDocker
	case "k8s", core.ServerRuntimeKubernetes:
		return core.ServerRuntimeKubernetes
	case core.ServerRuntimeOpenShift:
		return core.ServerRuntimeOpenShift
	case core.ServerRuntimeRelay:
		return core.ServerRuntimeRelay
	default:
		return strings.ToLower(strings.TrimSpace(runtime))
	}
}

func validateRelayAddress(value string) (string, string) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(value), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", "Enter an HTTP or HTTPS relay URL."
	}
	if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" {
		return "", "Use HTTPS for relay servers outside this host."
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "Relay URLs cannot contain credentials, query values, or fragments."
	}
	return parsed.String(), ""
}

func validateKubernetesServer(input *kubernetesServerRequest, existing *core.KubernetesServerConfig) (*core.KubernetesServerConfig, string) {
	if input == nil {
		return nil, "Paste a kubeconfig or provide a mounted kubeconfig path."
	}
	config := &core.KubernetesServerConfig{
		KubeconfigPath: strings.TrimSpace(input.KubeconfigPath),
		Context:        strings.TrimSpace(input.Context),
		Namespace:      strings.TrimSpace(input.Namespace),
	}
	if existing != nil {
		config.OpenShift = existing.OpenShift
	}
	source := strings.ToLower(strings.TrimSpace(input.Source))
	if source == "" {
		switch {
		case input.Kubeconfig != nil:
			source = "stored"
		case config.KubeconfigPath != "":
			source = "path"
		case existing != nil && existing.KubeconfigData != "":
			source = "stored"
		default:
			source = "path"
		}
	}
	var contextName string
	var err error
	switch source {
	case "stored":
		if existing != nil && existing.KubeconfigData != "" {
			config.KubeconfigData = existing.KubeconfigData
			config.CertificateAuthorityData = existing.CertificateAuthorityData
		}
		if input.Kubeconfig != nil && strings.TrimSpace(*input.Kubeconfig) != "" {
			config.KubeconfigData = *input.Kubeconfig
			if input.CertificateAuthority == nil {
				config.CertificateAuthorityData = ""
			}
		}
		if input.CertificateAuthority != nil {
			config.CertificateAuthorityData = strings.TrimSpace(*input.CertificateAuthority)
		}
		if strings.TrimSpace(config.KubeconfigData) == "" {
			return nil, "Paste the kubeconfig content before saving this server."
		}
		contextName, err = kubeconfig.ValidateStored([]byte(config.KubeconfigData), []byte(config.CertificateAuthorityData), config.Context)
		config.KubeconfigPath = ""
		config.KubeconfigStored = true
		config.CertificateAuthorityStored = config.CertificateAuthorityData != ""
	case "path":
		if config.KubeconfigPath == "" {
			return nil, "Enter the kubeconfig path mounted on the Dispatch controller."
		}
		contextName, err = validateKubeconfig(config.KubeconfigPath, config.Context)
	default:
		return nil, "Choose stored kubeconfig or mounted file."
	}
	if err != nil {
		return nil, err.Error()
	}
	config.Context = contextName
	if config.Namespace == "" {
		config.Namespace = "default"
	}
	return config, ""
}

func kubernetesAddress(config *core.KubernetesServerConfig) string {
	if config.KubeconfigStored {
		return "stored"
	}
	return config.KubeconfigPath
}

func (a *API) repairOpenShiftServer(w http.ResponseWriter, r *http.Request) {
	server, err := a.store.GetServer(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if server.Runtime != core.ServerRuntimeOpenShift || server.Kubernetes == nil || server.Kubernetes.OpenShift == nil {
		problem(w, http.StatusConflict, "Repair unavailable", "Only managed OpenShift connections can be repaired with oc login.")
		return
	}
	var input struct {
		LoginCommand string `json:"loginCommand"`
	}
	if !decode(w, r, &input) {
		return
	}
	previousTokenSecret := server.Kubernetes.OpenShift.TokenSecret
	result, err := a.openShift.Repair(r.Context(), input.LoginCommand)
	if err != nil {
		a.openShiftProblem(w, err)
		return
	}
	config := openshift.Config(result, server.Kubernetes.Namespace)
	server.Address, server.State, server.Kubernetes = result.Server, "ready", &config
	if err := a.store.UpdateServer(r.Context(), server); err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	cleanupContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := a.openShift.DeletePreviousToken(cleanupContext, result, previousTokenSecret); err != nil {
		a.logger.Warn("OpenShift repair left the previous managed token in place", "server_id", server.ID, "error", err)
	}
	writeJSON(w, http.StatusOK, server)
}

func (a *API) openShiftProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, openshift.ErrInvalidLoginCommand) {
		problem(w, http.StatusBadRequest, "OpenShift login command invalid", err.Error())
		return
	}
	problem(w, http.StatusBadGateway, "OpenShift connection failed", err.Error())
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	server, err := a.store.GetServer(r.Context(), id)
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if strings.EqualFold(server.Address, "local") {
		problem(w, http.StatusConflict, "Managed server cannot be deleted", "The controller Docker target remains managed and is marked unavailable whenever its socket is absent.")
		return
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, app := range apps {
		if app.ServerID == id {
			problem(w, http.StatusConflict, "Server in use", "Remove its applications before deleting this server.")
			return
		}
	}
	if server.Runtime == core.ServerRuntimeRelay {
		webhooks, listErr := a.store.ListRelayWebhooks(r.Context(), id)
		if listErr != nil {
			a.internal(w, listErr)
			return
		}
		if len(webhooks) > 0 {
			problem(w, http.StatusConflict, "Relay server in use", "Remove its webhook endpoints and connector bindings before deleting this relay.")
			return
		}
	}
	if err := a.store.DeleteServer(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

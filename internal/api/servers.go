package api

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/openshift"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/ssh"
)

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		project := r.URL.Query().Get("projectId")
		if project == "" {
			problem(w, 400, "Project required", "Specify projectId when listing assigned targets.")
			return
		}
		if !a.requireProject(w, r, core.PermissionInfrastructureInspect, project) {
			return
		}
		filtered := []core.Server{}
		for _, item := range items {
			allowed, err := a.assignedInfrastructure(r.Context(), project, "target", item.ID)
			if err != nil {
				a.internal(w, err)
				return
			}
			if allowed {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	writeJSON(w, 200, items)
}

type createServerRequest struct {
	Name        string                    `json:"name"`
	ProjectID   string                    `json:"projectId"`
	Address     string                    `json:"address"`
	Runtime     string                    `json:"runtime"`
	AgentNodeID string                    `json:"agentNodeId"`
	AgentMode   string                    `json:"agentMode"`
	Kubernetes  *kubernetesServerRequest  `json:"kubernetes"`
	Relay       *relayServerRequest       `json:"relay"`
	Builder     *core.BuilderServerConfig `json:"builder"`
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
	if a.auth.Hosted != nil && input.Runtime == core.ServerRuntimeKubernetes && input.ProjectID == "" {
		problem(w, http.StatusUnprocessableEntity, "Project required", "Choose the project whose worker will validate this cluster.")
		return
	}
	if a.auth.Hosted != nil && !a.hostedServerInput(w, input.Runtime, input.AgentNodeID, input.Kubernetes) {
		return
	}
	if input.AgentNodeID != "" && input.Runtime != core.ServerRuntimeDocker {
		problem(w, 422, "Agent target invalid", "Only Docker servers accept a runtime node binding.")
		return
	}
	if input.ProjectID != "" {
		if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
			a.notFoundOrInternal(w, err, "Project")
			return
		}
	}
	item := core.Server{ID: ulid.Make().String(), ProjectID: input.ProjectID, Name: input.Name, Runtime: input.Runtime, CreatedAt: time.Now().UTC()}
	switch input.Runtime {
	case core.ServerRuntimeDocker:
		if input.AgentNodeID != "" {
			if !a.bindRuntimeNode(w, r, &item, strings.TrimSpace(input.AgentNodeID)) {
				return
			}
			if input.Address == "" {
				input.Address = "agent:" + item.AgentNodeID
			}
		}
		if input.Address == "" {
			problem(w, http.StatusBadRequest, "Server address required", "Enter the Docker host name or IP address.")
			return
		}
		if strings.EqualFold(input.Address, "local") || item.AgentNodeID != "" && (strings.EqualFold(input.Address, "localhost") || strings.Trim(input.Address, "[]") == "::1" || input.Address == "127.0.0.1") {
			problem(w, http.StatusConflict, "Controller Docker is managed automatically", "Mount the Docker socket to register this controller.")
			return
		}
		item.Address, item.State, item.AgentMode = input.Address, "pending", "ssh-bootstrap"
		if item.AgentNodeID != "" {
			item.State, item.AgentMode = "ready", "outbound-runtime"
		}
	case core.ServerRuntimeKubernetes:
		kubernetes, detail := validateKubernetesServer(input.Kubernetes, nil)
		if detail != "" {
			problem(w, http.StatusBadRequest, "Kubernetes connection required", detail)
			return
		}
		if !a.inspectKubernetesTarget(w, r, kubernetes, nil, input.ProjectID) {
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
	case core.ServerRuntimeBuilder:
		if detail := a.validateBuilder(r.Context(), input.Address, input.Builder); detail != "" {
			problem(w, http.StatusBadRequest, "Builder connection invalid", detail)
			return
		}
		item.Address, item.State, item.AgentMode, item.Builder = input.Address, "ready", "ssh", input.Builder
	default:
		problem(w, http.StatusBadRequest, "Runtime unavailable", "Use docker, kubernetes, openshift, builder, or relay.")
		return
	}
	if err := a.store.CreateServer(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type updateServerRequest struct {
	Name       string                    `json:"name"`
	Address    string                    `json:"address"`
	Kubernetes *kubernetesServerRequest  `json:"kubernetes"`
	Relay      *relayServerRequest       `json:"relay"`
	Builder    *core.BuilderServerConfig `json:"builder"`
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
	if a.auth.Hosted != nil && !a.hostedServerInput(w, item.Runtime, item.AgentNodeID, input.Kubernetes) {
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
		if strings.EqualFold(input.Address, "local") || item.AgentNodeID != "" && (strings.EqualFold(input.Address, "localhost") || strings.Trim(input.Address, "[]") == "::1" || input.Address == "127.0.0.1") {
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
		if !a.inspectKubernetesTarget(w, r, kubernetes, item.Kubernetes, item.ProjectID) {
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
	case core.ServerRuntimeBuilder:
		if detail := a.validateBuilder(r.Context(), input.Address, input.Builder); detail != "" {
			problem(w, http.StatusBadRequest, "Builder connection invalid", detail)
			return
		}
		item.Address, item.Builder = input.Address, input.Builder
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
	case core.ServerRuntimeBuilder:
		return core.ServerRuntimeBuilder
	default:
		return strings.ToLower(strings.TrimSpace(runtime))
	}
}

func (a *API) validateBuilder(ctx context.Context, address string, config *core.BuilderServerConfig) string {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "ssh" || parsed.Hostname() == "" || parsed.User == nil || parsed.User.Username() == "" || parsed.User.String() != parsed.User.Username() || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "Enter an SSH URL such as ssh://builder@host.example.com."
	}
	if parsed.Port() != "" {
		port, err := strconv.Atoi(parsed.Port())
		if err != nil || port < 1 || port > 65535 {
			return "Enter a valid SSH port."
		}
	}
	if config == nil || config.SSHSecretID == "" || config.HostKey == "" || config.MaxConcurrent < 1 || config.MaxConcurrent > 16 {
		return "Choose an SSH key, paste the host public key, and set capacity from 1 to 16."
	}
	if _, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(config.HostKey)); err != nil || len(bytes.TrimSpace(rest)) != 0 {
		return "Paste the host public key in OpenSSH format."
	}
	secret, err := a.store.GetSecret(ctx, config.SSHSecretID)
	if err != nil || secret.Type != core.SecretTypeSSHPrivateKey {
		return "Choose a saved SSH private key."
	}
	return ""
}

func (a *API) scanBuilderSSHHost(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Address string `json:"address"`
	}
	if !decode(w, r, &input) {
		return
	}
	parsed, err := url.Parse(strings.TrimSpace(input.Address))
	if err != nil || parsed.Scheme != "ssh" || parsed.Hostname() == "" {
		problem(w, http.StatusBadRequest, "Builder address invalid", "Enter an SSH URL before checking the host key.")
		return
	}
	port := parsed.Port()
	if port == "" {
		port = "22"
	}
	fingerprint, key, err := scanSSHHost(r.Context(), net.JoinHostPort(parsed.Hostname(), port))
	if err != nil {
		problem(w, http.StatusBadGateway, "SSH host unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"fingerprint": fingerprint, "hostKey": key})
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
	if err := a.withStorageRegistrationRemoval(r, "server", id, func() error { return a.store.DeleteServer(r.Context(), id) }); err != nil {
		problem(w, 409, "Server deletion stopped", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

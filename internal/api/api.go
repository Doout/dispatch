package api

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/groups"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/openshift"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/ui"
	workflowservice "github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

type AuthConfig struct {
	AdminToken string
	Username   string
	Password   string
}

type EventConfig struct {
	WebhookSecret   string
	DefaultCommand  string
	GitHubAPIURL    string
	GitHubToken     string
	Vault           *secretcrypto.Vault
	GitHubApps      *githubapp.Manager
	SecretResolver  *secretvalue.Resolver
	Edge            *edge.Broker
	RepositoryCache string
}

type githubEventServices struct {
	events *events.Service
	groups *groups.Service
}

type API struct {
	handler        http.Handler
	store          store.Store
	deploy         *deploy.Service
	demo           bool
	auth           AuthConfig
	logger         *slog.Logger
	events         *events.Service
	groups         *groups.Service
	eventConfig    EventConfig
	secretResolver *secretvalue.Resolver
	edge           *edge.Broker
	openShift      *openshift.Bootstrapper
	lifecycle      events.Lifecycle
	githubMu       sync.Mutex
	githubServices map[string]githubEventServices
	manifestMu     sync.Mutex
	manifestStates map[string]githubAppManifestState
	workflows      *workflowservice.Service

	sessionMu sync.RWMutex
	sessions  map[string]time.Time
}

func New(data store.Store, deployments *deploy.Service, demo bool, auth AuthConfig, logger *slog.Logger, eventConfigs ...EventConfig) *API {
	eventConfig := EventConfig{DefaultCommand: "/preview"}
	if len(eventConfigs) > 0 {
		eventConfig = eventConfigs[0]
		if eventConfig.DefaultCommand == "" {
			eventConfig.DefaultCommand = "/preview"
		}
	}
	if eventConfig.Edge == nil && eventConfig.Vault != nil {
		eventConfig.Edge = edge.New(data, eventConfig.Vault)
	}
	if eventConfig.GitHubApps != nil && eventConfig.GitHubApps.Edge == nil {
		eventConfig.GitHubApps.Edge = eventConfig.Edge
	}
	if eventConfig.SecretResolver == nil && eventConfig.Vault != nil {
		eventConfig.SecretResolver = secretvalue.New(data, eventConfig.Vault)
	}
	if eventConfig.SecretResolver != nil && eventConfig.SecretResolver.Edge == nil {
		eventConfig.SecretResolver.Edge = eventConfig.Edge
	}
	var resolver events.PullRequestResolver
	var groupResolver groups.Resolver
	if eventConfig.GitHubAPIURL != "" || eventConfig.GitHubToken != "" {
		githubResolver := events.GitHubResolver{BaseURL: eventConfig.GitHubAPIURL, Token: eventConfig.GitHubToken}
		resolver, groupResolver = githubResolver, githubResolver
	}
	var notifier events.Notifier
	if eventConfig.GitHubToken != "" {
		notifier = events.GitHubNotifier{BaseURL: eventConfig.GitHubAPIURL, Token: eventConfig.GitHubToken}
	}
	lifecycle := events.DeploymentLifecycle{Store: data, Deployments: deployments}
	a := &API{store: data, deploy: deployments, demo: demo, auth: auth, logger: logger, sessions: make(map[string]time.Time),
		events: events.New(data, resolver, lifecycle, notifier), groups: groups.New(data, deployments, groupResolver, func() groups.Notifier {
			if value, ok := notifier.(groups.Notifier); ok {
				return value
			}
			return nil
		}(), nil), eventConfig: eventConfig, secretResolver: eventConfig.SecretResolver, openShift: openshift.New(), lifecycle: lifecycle,
		edge: eventConfig.Edge, githubServices: make(map[string]githubEventServices), manifestStates: make(map[string]githubAppManifestState)}
	a.workflows = workflowservice.NewService(data, eventConfig.GitHubApps, eventConfig.SecretResolver, deployments, logger, eventConfig.RepositoryCache)
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Use(a.logRequest)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/relay/install.sh", a.relayInstallScript)
	r.Get("/relay/bin/{platform}", a.relayBinary)
	r.Get("/edge/install.sh", a.edgeInstallScript)
	r.Get("/edge/bin/{platform}", a.edgeBinary)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/auth/status", a.authStatus)
		r.Post("/auth/setup", a.setupAdmin)
		r.Post("/auth/login", a.login)
		r.Post("/events/github", a.githubWebhook)
		r.Post("/events/github/apps/{id}", a.githubAppWebhook)
		r.Get("/github-apps/manifest/callback", a.completeGitHubAppManifest)
		r.Get("/edge/nodes/{id}/jobs/next", a.leaseEdgeJob)
		r.Post("/edge/nodes/{id}/jobs/{jobId}/complete", a.completeEdgeJob)
		r.Group(func(r chi.Router) {
			r.Use(a.authorize)
			r.Get("/overview", a.overview)
			r.Get("/secrets", a.listSecrets)
			r.Post("/secrets", a.createSecret)
			r.Put("/secrets/{id}", a.updateSecret)
			r.Delete("/secrets/{id}", a.deleteSecret)
			r.Get("/secret-stores", a.listSecretStores)
			r.Post("/secret-stores", a.createSecretStore)
			r.Put("/secret-stores/{id}", a.updateSecretStore)
			r.Post("/secret-stores/{id}/verify", a.verifySecretStore)
			r.Delete("/secret-stores/{id}", a.deleteSecretStore)
			r.Get("/private-networks", a.listPrivateNetworks)
			r.Post("/private-networks", a.createPrivateNetwork)
			r.Put("/private-networks/{id}", a.updatePrivateNetwork)
			r.Post("/private-networks/{id}/verify", a.verifyPrivateNetwork)
			r.Post("/private-networks/{id}/rotate-token", a.rotateEdgeToken)
			r.Post("/private-networks/{id}/install-connector", a.installLanewayConnector)
			r.Delete("/private-networks/{id}", a.deletePrivateNetwork)
			r.Get("/github-apps", a.listGitHubApps)
			r.Post("/github-apps", a.createGitHubApp)
			r.Put("/github-apps/{id}", a.updateGitHubApp)
			r.Delete("/github-apps/{id}", a.deleteGitHubApp)
			r.Post("/github-apps/{id}/verify", a.verifyGitHubApp)
			r.Get("/github-apps/{id}/installations", a.listGitHubAppInstallations)
			r.Get("/github-apps/{id}/repositories", a.listGitHubAppRepositories)
			r.Post("/github-apps/manifest", a.startGitHubAppManifest)
			r.Get("/config-sources", a.listConfigSources)
			r.Post("/config-sources", a.createConfigSource)
			r.Put("/config-sources/{id}", a.updateConfigSource)
			r.Post("/config-sources/{id}/sync", a.syncConfigSource)
			r.Delete("/config-sources/{id}", a.deleteConfigSource)
			r.Get("/workflow/resources", a.listWorkflowResources)
			r.Get("/workflow/resources/{id}/topology", a.getWorkflowTopology)
			r.Post("/workflow/resources/{id}/activate", a.activateWorkflowResource)
			r.Post("/workflow/resources/{id}/deactivate", a.deactivateWorkflowResource)
			r.Post("/workflow/resources/{id}/runs", a.runWorkflowResource)
			r.Get("/workflow/revisions", a.listWorkflowRevisions)
			r.Get("/workflow/revisions/{id}", a.getWorkflowRevision)
			r.Get("/workflow/revisions/{id}/jobs", a.listWorkflowJobs)
			r.Get("/workflow/revisions/{id}/stages", a.listWorkflowStages)
			r.Post("/workflow/stages/{id}/approve", a.approveWorkflowStage)
			r.Post("/workflow/validate", a.validateWorkflowDocument)
			r.Get("/projects", a.listProjects)
			r.Post("/projects", a.createProject)
			r.Put("/projects/{id}", a.updateProject)
			r.Delete("/projects/{id}", a.deleteProject)
			r.Get("/servers", a.listServers)
			r.Post("/servers", a.createServer)
			r.Post("/relay/ssh/scan", a.scanRelaySSHHost)
			r.Post("/relay/ssh/install", a.installRelayOverSSH)
			r.Put("/servers/{id}", a.updateServer)
			r.Post("/servers/{id}/relay/verify", a.verifyRelayServer)
			r.Get("/servers/{id}/relay/webhooks", a.listRelayWebhooks)
			r.Post("/servers/{id}/relay/webhooks", a.createRelayWebhook)
			r.Delete("/servers/{id}/relay/webhooks/{webhookId}", a.deleteRelayWebhook)
			r.Post("/servers/{id}/repair", a.repairOpenShiftServer)
			r.Delete("/servers/{id}", a.deleteServer)
			r.Get("/apps", a.listApps)
			r.Post("/helm/inspect", a.inspectHelmSource)
			r.Post("/apps", a.createApp)
			r.Get("/apps/{id}/helm-values", a.getAppHelmValues)
			r.Put("/apps/{id}/helm-values", a.updateAppHelmValues)
			r.Put("/apps/{id}/hooks", a.updateAppHooks)
			r.Delete("/apps/{id}", a.deleteApp)
			r.Get("/event-triggers", a.listEventTriggers)
			r.Post("/apps/{id}/event-triggers", a.createEventTrigger)
			r.Put("/event-triggers/{id}", a.updateEventTrigger)
			r.Delete("/event-triggers/{id}", a.deleteEventTrigger)
			r.Get("/preview-environments", a.listPreviewEnvironments)
			r.Get("/preview-groups", a.listPreviewGroups)
			r.Post("/preview-groups", a.createPreviewGroup)
			r.Get("/preview-groups/{id}", a.getPreviewGroup)
			r.Put("/preview-groups/{id}", a.updatePreviewGroup)
			r.Delete("/preview-groups/{id}", a.deletePreviewGroup)
			r.Get("/preview-group-runs", a.listPreviewGroupRuns)
			r.Get("/preview-group-runs/{id}", a.getPreviewGroupRun)
			r.Post("/preview-group-runs/{id}/cleanup", a.cleanupPreviewGroupRun)
			r.Post("/apps/{id}/cleanup", a.cleanupApp)
			r.Get("/deployments", a.listDeployments)
			r.Get("/deployments/{id}", a.getDeployment)
			r.Get("/deployments/{id}/topology", a.getDeploymentTopology)
			r.Get("/deployments/{id}/manifests", a.getDeploymentManifests)
			r.Get("/deployments/{id}/logs", a.getDeploymentLogs)
			r.Post("/deployments/{id}/cancel", a.cancelDeployment)
			r.Get("/deployments/{id}/events", a.deploymentEvents)
			r.Get("/servers/{id}/topology", a.getServerTopology)
			r.Post("/apps/{id}/deployments", a.startDeployment)
			r.Get("/contracts/provider", a.providerContract)
			r.Get("/contracts/runtime", a.runtimeContract)
		})
	})
	r.Handle("/*", ui.Handler())
	r.Handle("/", ui.Handler())
	a.handler = r
	return a
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }

func (a *API) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.validBearer(r) {
			next.ServeHTTP(w, r)
			return
		}

		username, password, basic := r.BasicAuth()
		valid, setupRequired, err := a.validPassword(r.Context(), username, password)
		if err != nil {
			a.internal(w, err)
			return
		}
		if basic && valid {
			next.ServeHTTP(w, r)
			return
		}
		unauthorized(w, setupRequired)
	})
}

func (a *API) validBearer(r *http.Request) bool {
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	if a.auth.AdminToken != "" && secureEqual(provided, a.auth.AdminToken) {
		return true
	}
	a.sessionMu.RLock()
	expires, found := a.sessions[provided]
	a.sessionMu.RUnlock()
	if found && time.Now().Before(expires) {
		return true
	}
	valid, err := a.store.AdminSessionValid(r.Context(), sessionHash(provided), time.Now().UTC())
	return err == nil && valid
}

func secureEqual(left, right string) bool {
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func unauthorized(w http.ResponseWriter, setupRequired bool) {
	if setupRequired {
		problem(w, http.StatusUnauthorized, "Administrator setup required", "Create the first administrator account before using Dispatch.")
		return
	}
	problem(w, http.StatusUnauthorized, "Authentication required", "Enter the administrator username and password.")
}

func (a *API) validPassword(ctx context.Context, username, password string) (valid, setupRequired bool, err error) {
	if a.auth.Username != "" {
		return secureEqual(username, a.auth.Username) && secureEqual(password, a.auth.Password), false, nil
	}
	credential, err := a.store.GetAdminCredential(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	valid = secureEqual(username, credential.Username) && bcrypt.CompareHashAndPassword([]byte(credential.PasswordHash), []byte(password)) == nil
	return valid, false, nil
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var input setupAdminRequest
	if !decode(w, r, &input) {
		return
	}
	valid, setupRequired, err := a.validPassword(r.Context(), strings.TrimSpace(input.Username), input.Password)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !valid {
		unauthorized(w, setupRequired)
		return
	}
	token, err := a.createSession(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (a *API) createSession(ctx context.Context) (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	now := time.Now().UTC()
	expires := now.Add(12 * time.Hour)
	if err := a.store.CreateAdminSession(ctx, sessionHash(token), expires, now); err != nil {
		return "", err
	}
	a.sessionMu.Lock()
	for existing, expires := range a.sessions {
		if now.After(expires) {
			delete(a.sessions, existing)
		}
	}
	a.sessions[token] = expires
	a.sessionMu.Unlock()
	return token, nil
}

func sessionHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (a *API) authStatus(w http.ResponseWriter, r *http.Request) {
	configured := a.auth.Username != ""
	if !configured {
		_, err := a.store.GetAdminCredential(r.Context())
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			a.internal(w, err)
			return
		}
		configured = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"setupRequired":       !configured,
		"tokenLoginAvailable": a.auth.AdminToken != "",
	})
}

type setupAdminRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *API) setupAdmin(w http.ResponseWriter, r *http.Request) {
	if a.auth.Username != "" {
		problem(w, http.StatusConflict, "Administrator already configured", "Environment-provided credentials are active on this controller.")
		return
	}
	if _, err := a.store.GetAdminCredential(r.Context()); err == nil {
		problem(w, http.StatusConflict, "Administrator already configured", "The first administrator account has already been created.")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.internal(w, err)
		return
	}

	var input setupAdminRequest
	if !decode(w, r, &input) {
		return
	}
	input.Username = strings.TrimSpace(input.Username)
	if detail := validateCredentials(input.Username, input.Password); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid administrator credentials", detail)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		a.internal(w, err)
		return
	}
	err = a.store.CreateAdminCredential(r.Context(), store.AdminCredential{
		Username: input.Username, PasswordHash: string(hash), CreatedAt: time.Now().UTC(),
	})
	if errors.Is(err, store.ErrAlreadyExists) {
		problem(w, http.StatusConflict, "Administrator already configured", "The first administrator account has already been created.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"username": input.Username})
}

func validateCredentials(username, password string) string {
	if len(username) < 3 || len(username) > 64 || strings.Contains(username, ":") {
		return "Use a username between 3 and 64 characters without a colon."
	}
	if len([]byte(password)) < 12 || len([]byte(password)) > 72 {
		return "Use a password between 12 and 72 bytes."
	}
	return ""
}

func (a *API) overview(w http.ResponseWriter, r *http.Request) {
	projects, err := a.store.ListProjects(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	servers, err := a.store.ListServers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	deployments, err := a.store.ListDeployments(r.Context(), 100)
	if err != nil {
		a.internal(w, err)
		return
	}
	eventTriggers, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	previews, err := a.store.ListPreviewEnvironments(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	previewGroups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	previewGroupRuns, err := a.store.ListPreviewGroupRuns(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	secrets, err := a.store.ListSecrets(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	secretStores, err := a.store.ListSecretStores(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	privateNetworks, err := a.store.ListPrivateNetworks(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	githubApps, err := a.store.ListGitHubApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	relayWebhooks, err := a.store.ListRelayWebhooks(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	configSources, err := a.store.ListConfigSources(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	workflowResources, err := a.store.ListWorkflowResources(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	currentWorkflowResources := workflowResources[:0]
	for _, resource := range workflowResources {
		if resource.State != "removed" {
			currentWorkflowResources = append(currentWorkflowResources, resource)
		}
	}
	workflowResources = currentWorkflowResources
	for index := range workflowResources {
		documents, parseErr := workflowservice.Parse(workflowResources[index].Path, []byte(workflowResources[index].Document))
		if parseErr != nil || len(documents) != 1 {
			continue
		}
		if documents[0].Spec != nil {
			workflowResources[index].SourceCount = len(documents[0].Spec.Sources)
			workflowResources[index].JobCount = len(documents[0].Spec.Jobs)
			for _, stage := range documents[0].Spec.Stages {
				workflowResources[index].StageNames = append(workflowResources[index].StageNames, stage.Name)
				if !slices.Contains(workflowResources[index].TargetRefs, stage.TargetRef) {
					workflowResources[index].TargetRefs = append(workflowResources[index].TargetRefs, stage.TargetRef)
				}
			}
		} else if documents[0].Pipeline != nil {
			workflowResources[index].SourceCount = len(documents[0].Pipeline.Sources)
			workflowResources[index].JobCount = len(documents[0].Pipeline.Jobs)
		}
	}
	workflowRevisions, err := a.store.ListWorkflowRevisions(r.Context(), "", 100)
	if err != nil {
		a.internal(w, err)
		return
	}
	workflowStageRuns, err := a.store.ListWorkflowStageRuns(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, core.Overview{Demo: a.demo, SecretStorageConfigured: a.eventConfig.Vault != nil, Projects: projects, Servers: servers, Apps: apps, Deployments: deployments,
		EventTriggers: eventTriggers, Previews: previews, PreviewGroups: previewGroups, PreviewGroupRuns: previewGroupRuns, Secrets: secrets, SecretStores: secretStores, PrivateNetworks: privateNetworks, GitHubApps: githubApps, RelayWebhooks: relayWebhooks,
		ConfigSources: configSources, WorkflowResources: workflowResources, WorkflowRevisions: workflowRevisions, WorkflowStageRuns: workflowStageRuns})
}

func (a *API) RunWorkflowPoller(ctx context.Context) {
	if a.workflows != nil {
		a.workflows.RunPoller(ctx)
	}
}

type secretRequest struct {
	Name                string  `json:"name"`
	Type                string  `json:"type"`
	Source              string  `json:"source"`
	EnvironmentVariable string  `json:"environmentVariable"`
	Value               *string `json:"value"`
	Generate            bool    `json:"generate"`
	ExternalStoreID     string  `json:"externalStoreId"`
	ExternalSecretID    string  `json:"externalSecretId"`
	ExternalField       string  `json:"externalField"`
}

var environmentVariablePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validateSecretInput(name string, secretType core.SecretType, environmentVariable string, value *string, requireValue bool) string {
	if name == "" || len(name) > 80 {
		return "Enter a name no longer than 80 characters."
	}
	if !core.ValidSecretType(secretType) {
		return "Choose a supported secret type."
	}
	if !environmentVariablePattern.MatchString(environmentVariable) {
		return "Use a valid environment variable name."
	}
	if strings.HasPrefix(environmentVariable, "DISPATCH_") {
		return "DISPATCH_ variables are reserved by the controller."
	}
	if requireValue && (value == nil || *value == "") {
		return "Enter a secret value."
	}
	if value != nil && len(*value) > 64<<10 {
		return "Keep the secret value under 64 KiB."
	}
	if value != nil && *value != "" && secretType == core.SecretTypeSSHPrivateKey {
		if _, err := sshPublicKey(*value); err != nil {
			return err.Error()
		}
	}
	return ""
}

func normalizeSecretType(value string) core.SecretType {
	if strings.TrimSpace(value) == "" {
		return core.SecretTypeText
	}
	return core.SecretType(strings.TrimSpace(value))
}

func normalizeSecretSource(value string) core.SecretSource {
	if strings.TrimSpace(value) == "" {
		return core.SecretSourceLocal
	}
	return core.SecretSource(strings.TrimSpace(value))
}

func (a *API) validateExternalSecret(ctx context.Context, input secretRequest) string {
	if strings.TrimSpace(input.ExternalStoreID) == "" {
		return "Choose a secret store."
	}
	if strings.TrimSpace(input.ExternalSecretID) == "" || len(input.ExternalSecretID) > 2048 {
		return "Enter the secret ID from the provider."
	}
	if len(input.ExternalField) > 256 {
		return "Keep the value field under 256 characters."
	}
	item, err := a.store.GetSecretStore(ctx, strings.TrimSpace(input.ExternalStoreID))
	if err != nil {
		return "Choose an available secret store."
	}
	if item.State != "ready" {
		return "Verify the secret store before using it."
	}
	return ""
}

func generateSSHKey() (privateKey, publicKey string, err error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(private, "dispatch")
	if err != nil {
		return "", "", err
	}
	public, err := ssh.NewPublicKey(private.Public())
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(block)), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), nil
}

func sshPublicKey(privateKey string) (string, error) {
	parsed, err := ssh.ParseRawPrivateKey([]byte(strings.TrimSpace(privateKey)))
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return "", errors.New("Use an unencrypted SSH private key so deployments can run without a passphrase")
		}
		return "", errors.New("Enter a valid OpenSSH or PEM private key")
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return "", errors.New("The SSH private key uses an unsupported format")
	}
	public, err := ssh.NewPublicKey(signer.Public())
	if err != nil {
		return "", errors.New("Could not derive the SSH public key")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), nil
}

func (a *API) listSecrets(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListSecrets(r.Context())
	a.list(w, items, err)
}

func (a *API) createSecret(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before saving credentials.")
		return
	}
	var input secretRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.EnvironmentVariable = strings.TrimSpace(input.Name), strings.TrimSpace(input.EnvironmentVariable)
	secretType := normalizeSecretType(input.Type)
	secretSource := normalizeSecretSource(input.Source)
	if input.Generate {
		if secretSource != core.SecretSourceLocal || secretType != core.SecretTypeSSHPrivateKey {
			problem(w, http.StatusBadRequest, "Invalid secret", "Only SSH private keys can be generated.")
			return
		}
		privateKey, _, err := generateSSHKey()
		if err != nil {
			a.internal(w, err)
			return
		}
		input.Value = &privateKey
	}
	if secretSource != core.SecretSourceLocal && secretSource != core.SecretSourceExternal {
		problem(w, http.StatusBadRequest, "Invalid secret", "Choose local or external storage.")
		return
	}
	if detail := validateSecretInput(input.Name, secretType, input.EnvironmentVariable, input.Value, secretSource == core.SecretSourceLocal); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid secret", detail)
		return
	}
	if secretSource == core.SecretSourceExternal {
		if input.Value != nil && *input.Value != "" {
			problem(w, http.StatusBadRequest, "Invalid secret", "External secret references do not accept a local value.")
			return
		}
		if detail := a.validateExternalSecret(r.Context(), input); detail != "" {
			problem(w, http.StatusBadRequest, "Invalid secret", detail)
			return
		}
	}
	now := time.Now().UTC()
	item := core.Secret{ID: ulid.Make().String(), Name: input.Name, Type: secretType, Source: secretSource, EnvironmentVariable: input.EnvironmentVariable,
		ExternalStoreID: strings.TrimSpace(input.ExternalStoreID), ExternalSecretID: strings.TrimSpace(input.ExternalSecretID), ExternalField: strings.TrimSpace(input.ExternalField), CreatedAt: now, UpdatedAt: now}
	if item.Source == core.SecretSourceLocal && item.Type == core.SecretTypeSSHPrivateKey {
		item.PublicValue, _ = sshPublicKey(*input.Value)
	}
	if item.Source == core.SecretSourceLocal {
		encrypted, err := a.eventConfig.Vault.Encrypt("secret:"+item.ID, []byte(*input.Value))
		if err != nil {
			a.internal(w, err)
			return
		}
		item.EncryptedValue = encrypted
	} else {
		item.EncryptedValue = ""
	}
	if err := a.store.CreateSecret(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateSecret(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before saving credentials.")
		return
	}
	item, err := a.store.GetSecret(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Secret")
		return
	}
	var input secretRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.EnvironmentVariable = strings.TrimSpace(input.Name), strings.TrimSpace(input.EnvironmentVariable)
	secretType := item.Type
	if strings.TrimSpace(input.Type) != "" {
		secretType = normalizeSecretType(input.Type)
	}
	secretSource := item.Source
	if secretSource == "" {
		secretSource = core.SecretSourceLocal
	}
	if strings.TrimSpace(input.Source) != "" {
		secretSource = normalizeSecretSource(input.Source)
	}
	if input.Generate {
		if secretSource != core.SecretSourceLocal || secretType != core.SecretTypeSSHPrivateKey {
			problem(w, http.StatusBadRequest, "Invalid secret", "Only SSH private keys can be generated.")
			return
		}
		privateKey, _, generateErr := generateSSHKey()
		if generateErr != nil {
			a.internal(w, generateErr)
			return
		}
		input.Value = &privateKey
	}
	if secretSource != core.SecretSourceLocal && secretSource != core.SecretSourceExternal {
		problem(w, http.StatusBadRequest, "Invalid secret", "Choose local or external storage.")
		return
	}
	validationValue := input.Value
	if secretSource == core.SecretSourceLocal && (validationValue == nil || *validationValue == "") && (secretType != item.Type || item.Source == core.SecretSourceExternal) {
		if item.Source == core.SecretSourceExternal {
			problem(w, http.StatusBadRequest, "Invalid secret", "Enter a value when moving an external secret into Dispatch.")
			return
		}
		plaintext, decryptErr := a.secretResolver.Resolve(r.Context(), item.ID)
		if decryptErr != nil {
			a.internal(w, decryptErr)
			return
		}
		current := string(plaintext)
		validationValue = &current
	}
	if detail := validateSecretInput(input.Name, secretType, input.EnvironmentVariable, validationValue, false); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid secret", detail)
		return
	}
	if secretSource == core.SecretSourceExternal {
		if detail := a.validateExternalSecret(r.Context(), input); detail != "" {
			problem(w, http.StatusBadRequest, "Invalid secret", detail)
			return
		}
	}
	item.Name, item.Type, item.Source, item.EnvironmentVariable, item.UpdatedAt = input.Name, secretType, secretSource, input.EnvironmentVariable, time.Now().UTC()
	if secretSource == core.SecretSourceLocal && input.Value != nil && *input.Value != "" {
		item.EncryptedValue, err = a.eventConfig.Vault.Encrypt("secret:"+item.ID, []byte(*input.Value))
		if err != nil {
			a.internal(w, err)
			return
		}
	}
	if secretSource == core.SecretSourceExternal {
		item.EncryptedValue, item.PublicValue = "", ""
		item.ExternalStoreID, item.ExternalSecretID, item.ExternalField = strings.TrimSpace(input.ExternalStoreID), strings.TrimSpace(input.ExternalSecretID), strings.TrimSpace(input.ExternalField)
	} else if item.Type == core.SecretTypeSSHPrivateKey {
		value := validationValue
		if input.Value != nil && *input.Value != "" {
			value = input.Value
		}
		if value != nil {
			item.PublicValue, _ = sshPublicKey(*value)
		}
	} else {
		item.PublicValue = ""
	}
	if secretSource == core.SecretSourceLocal {
		item.ExternalStoreID, item.ExternalSecretID, item.ExternalField = "", "", ""
	}
	if err := a.store.UpdateSecret(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Secret")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteSecret(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, app := range apps {
		if app.SourceCredentialID == id {
			problem(w, http.StatusConflict, "Credential in use", "Remove this credential from the application source before deleting it.")
			return
		}
		if slices.Contains(app.HookSecretIDs, id) {
			problem(w, http.StatusConflict, "Credential in use", "Detach this credential from the application build hook before deleting it.")
			return
		}
	}
	triggers, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if slices.Contains(trigger.SecretIDs, id) {
			problem(w, http.StatusConflict, "Credential in use", "Detach this credential from the event build hook before deleting it.")
			return
		}
	}
	groups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, group := range groups {
		for _, component := range group.Components {
			if slices.Contains(component.SecretIDs, id) {
				problem(w, http.StatusConflict, "Credential in use", "Detach this credential from the preview group build hook before deleting it.")
				return
			}
		}
	}
	if err := a.store.DeleteSecret(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListProjects(r.Context())
	a.list(w, items, err)
}
func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListServers(r.Context())
	a.list(w, items, err)
}
func (a *API) listApps(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListApps(r.Context())
	a.list(w, items, err)
}

func (a *API) listEventTriggers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListEventTriggers(r.Context(), strings.TrimSpace(r.URL.Query().Get("appId")))
	a.list(w, items, err)
}

type createEventTriggerRequest struct {
	Provider       string   `json:"provider"`
	GitHubAppID    *string  `json:"githubAppId"`
	Repository     string   `json:"repository"`
	Command        string   `json:"command"`
	Enabled        *bool    `json:"enabled"`
	PreDeployHook  string   `json:"preDeployHook"`
	PostDeployHook string   `json:"postDeployHook"`
	SecretIDs      []string `json:"secretIds"`
}

func (a *API) validateSecretIDs(ctx context.Context, ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return errors.New("secret bindings must be unique")
		}
		seen[id] = true
		if _, err := a.store.GetSecret(ctx, id); err != nil {
			return errors.New("one or more selected secrets no longer exist")
		}
	}
	return nil
}

const maxEventHookBytes = 64 << 10

func validateEventHooks(preDeployHook, postDeployHook string) error {
	if len(preDeployHook) > maxEventHookBytes || len(postDeployHook) > maxEventHookBytes {
		return fmt.Errorf("each deployment hook must be no larger than 64 KiB")
	}
	if strings.ContainsRune(preDeployHook+postDeployHook, 0) {
		return errors.New("deployment hooks must contain plain shell text")
	}
	return nil
}

func (a *API) createEventTrigger(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	if _, err := a.store.GetApp(r.Context(), appID); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	var input createEventTriggerRequest
	if !decode(w, r, &input) {
		return
	}
	if err := validateEventHooks(input.PreDeployHook, input.PostDeployHook); err != nil {
		problem(w, http.StatusBadRequest, "Invalid deployment hook", err.Error())
		return
	}
	if err := a.validateSecretIDs(r.Context(), input.SecretIDs); err != nil {
		problem(w, http.StatusBadRequest, "Invalid secret binding", err.Error())
		return
	}
	providerName := strings.ToLower(strings.TrimSpace(input.Provider))
	if providerName == "" {
		providerName = string(core.EventProviderGitHub)
	}
	if providerName != string(core.EventProviderGitHub) {
		problem(w, http.StatusBadRequest, "Event provider unavailable", "Use the github provider for pull request preview events.")
		return
	}
	repository := events.NormalizeRepository(input.Repository)
	if owner, name, ok := strings.Cut(repository, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		problem(w, http.StatusBadRequest, "Repository required", "Use an owner/repository identifier.")
		return
	}
	githubAppID := ""
	if input.GitHubAppID != nil {
		githubAppID = strings.TrimSpace(*input.GitHubAppID)
	}
	if githubAppID != "" {
		if err := a.validateGitHubRepositoryAccess(r.Context(), githubAppID, repository); err != nil {
			problem(w, http.StatusBadRequest, "Repository is not connected", err.Error())
			return
		}
	}
	command, err := events.NormalizeCommand(input.Command, a.eventConfig.DefaultCommand)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid trigger command", err.Error())
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	requested := core.EventTrigger{ID: ulid.Make().String(), AppID: appID, GitHubAppID: githubAppID, Provider: core.EventProvider(providerName),
		Repository: repository, Command: command, Enabled: enabled, PreDeployHook: input.PreDeployHook,
		PostDeployHook: input.PostDeployHook, SecretIDs: input.SecretIDs, CreatedAt: now, UpdatedAt: now}
	item, created, err := a.store.CreateEventTrigger(r.Context(), requested)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !created && (item.GitHubAppID != requested.GitHubAppID || item.Command != requested.Command || item.Enabled != requested.Enabled || item.PreDeployHook != requested.PreDeployHook || item.PostDeployHook != requested.PostDeployHook || !slices.Equal(item.SecretIDs, requested.SecretIDs)) {
		problem(w, http.StatusConflict, "Event trigger already exists", "Update the existing event rule to change its command, state, or deployment hooks.")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (a *API) updateEventTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	items, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	var item *core.EventTrigger
	for index := range items {
		if items[index].ID == id {
			item = &items[index]
			break
		}
	}
	if item == nil {
		problem(w, http.StatusNotFound, "Event trigger not found", "The requested event trigger does not exist.")
		return
	}
	var input createEventTriggerRequest
	if !decode(w, r, &input) {
		return
	}
	if err := validateEventHooks(input.PreDeployHook, input.PostDeployHook); err != nil {
		problem(w, http.StatusBadRequest, "Invalid deployment hook", err.Error())
		return
	}
	if err := a.validateSecretIDs(r.Context(), input.SecretIDs); err != nil {
		problem(w, http.StatusBadRequest, "Invalid secret binding", err.Error())
		return
	}
	command, err := events.NormalizeCommand(input.Command, item.Command)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid trigger command", err.Error())
		return
	}
	if input.GitHubAppID != nil {
		githubAppID := strings.TrimSpace(*input.GitHubAppID)
		if githubAppID != "" {
			if err := a.validateGitHubRepositoryAccess(r.Context(), githubAppID, item.Repository); err != nil {
				problem(w, http.StatusBadRequest, "Repository is not connected", err.Error())
				return
			}
		}
		item.GitHubAppID = githubAppID
	}
	enabled := item.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	item.Command = command
	item.Enabled = enabled
	item.PreDeployHook = input.PreDeployHook
	item.PostDeployHook = input.PostDeployHook
	item.SecretIDs = input.SecretIDs
	item.UpdatedAt = time.Now().UTC()
	if err := a.store.UpdateEventTrigger(r.Context(), *item); err != nil {
		a.notFoundOrInternal(w, err, "Event trigger")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteEventTrigger(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteEventTrigger(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.notFoundOrInternal(w, err, "Event trigger")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listPreviewEnvironments(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPreviewEnvironments(r.Context(), strings.TrimSpace(r.URL.Query().Get("appId")))
	a.list(w, items, err)
}

const maxWebhookBytes = 1 << 20

func (a *API) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.WebhookSecret == "" {
		problem(w, http.StatusServiceUnavailable, "Webhook receiver is not configured", "Set a webhook secret before sending events.")
		return
	}
	a.processGitHubWebhook(w, r, a.eventConfig.WebhookSecret, "", 0, a.groups, a.events)
}

func (a *API) listDeployments(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := a.store.ListDeployments(r.Context(), limit)
	a.list(w, items, err)
}

func (a *API) list(w http.ResponseWriter, value any, err error) {
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

type createProjectRequest struct{ Name, Description string }

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	var input createProjectRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		problem(w, http.StatusBadRequest, "Project name required", "Enter a name before creating the project.")
		return
	}
	item := core.Project{ID: ulid.Make().String(), Name: input.Name, Description: strings.TrimSpace(input.Description), CreatedAt: time.Now().UTC()}
	if err := a.store.CreateProject(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	var input createProjectRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" {
		problem(w, http.StatusBadRequest, "Project name required", "Enter a name before saving the project.")
		return
	}
	projects, err := a.store.ListProjects(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, project := range projects {
		if project.ID != item.ID && strings.EqualFold(project.Name, input.Name) {
			problem(w, http.StatusConflict, "Project name already used", "Choose another project name.")
			return
		}
	}
	item.Name, item.Description = input.Name, strings.TrimSpace(input.Description)
	if err := a.store.UpdateProject(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetProject(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, app := range apps {
		if app.ProjectID == id {
			problem(w, http.StatusConflict, "Project in use", "Remove its applications before deleting this project.")
			return
		}
	}
	if err := a.store.DeleteProject(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

type createAppRequest struct {
	ProjectID, ServerID, Name, SourceRepo, Branch, BuildType, ContextPath, DockerfilePath, ComposePath, ComposeContent, Domain string
	SourceAuthType, SourceCredentialID                                                                                         string
	HelmChart, HelmVersion, HelmRepository, HelmValues, HelmNamespace, HelmRelease                                             string
	HelmValueOverrides                                                                                                         map[string]interface{}
	PreDeployHook, PostDeployHook                                                                                              string
	ContainerPort                                                                                                              int
	Template                                                                                                                   bool
}

const maxComposeContentBytes = 512 * 1024
const maxHookBytes = 64 * 1024

func (a *API) createApp(w http.ResponseWriter, r *http.Request) {
	var input createAppRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.SourceRepo = strings.TrimSpace(input.SourceRepo)
	input.SourceAuthType = strings.TrimSpace(input.SourceAuthType)
	input.SourceCredentialID = strings.TrimSpace(input.SourceCredentialID)
	input.ComposeContent = strings.TrimSpace(input.ComposeContent)
	input.HelmChart = strings.TrimSpace(input.HelmChart)
	input.HelmVersion = strings.TrimSpace(input.HelmVersion)
	input.HelmRepository = strings.TrimSpace(input.HelmRepository)
	input.HelmNamespace = strings.TrimSpace(input.HelmNamespace)
	input.HelmRelease = strings.TrimSpace(input.HelmRelease)
	input.PreDeployHook = strings.TrimSpace(input.PreDeployHook)
	input.PostDeployHook = strings.TrimSpace(input.PostDeployHook)
	directCompose := input.ComposeContent != ""
	helmApplication := input.BuildType == string(core.BuildTypeHelm)
	if input.HelmValueOverrides != nil {
		if strings.TrimSpace(input.HelmValues) != "" {
			problem(w, http.StatusBadRequest, "Choose one Helm values format", "Send structured Helm value overrides or raw Helm values, not both.")
			return
		}
		encoded, err := encodeHelmValueOverrides(input.HelmValueOverrides)
		if err != nil {
			problem(w, http.StatusBadRequest, "Helm values unavailable", err.Error())
			return
		}
		input.HelmValues = encoded
	}
	if input.ProjectID == "" || input.ServerID == "" || input.Name == "" {
		problem(w, http.StatusBadRequest, "Application details required", "Choose a project and server, then enter an application name.")
		return
	}
	if input.SourceRepo == "" && !directCompose && !helmApplication {
		problem(w, http.StatusBadRequest, "Application source required", "Enter a repository URL, paste a Docker Compose file, or configure a Helm chart.")
		return
	}
	if detail := validateSourceAuthentication(input.SourceRepo, input.SourceAuthType, input.SourceCredentialID); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid source authentication", detail)
		return
	}
	if input.SourceCredentialID != "" {
		if a.eventConfig.Vault == nil {
			problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before attaching repository credentials.")
			return
		}
		if input.SourceAuthType == deploy.SourceAuthGitHubApp {
			connection, err := a.store.GetGitHubApp(r.Context(), input.SourceCredentialID)
			if err != nil {
				a.notFoundOrInternal(w, err, "GitHub App")
				return
			}
			if connection.State != "ready" {
				problem(w, http.StatusConflict, "GitHub App not ready", "Install and verify this GitHub App before using it for a repository.")
				return
			}
			if err := githubapp.ValidateRepositoryHost(input.SourceRepo, connection.WebURL); err != nil {
				problem(w, http.StatusBadRequest, "GitHub App host mismatch", err.Error())
				return
			}
		} else {
			secret, err := a.store.GetSecret(r.Context(), input.SourceCredentialID)
			if err != nil {
				a.notFoundOrInternal(w, err, "Source credential")
				return
			}
			if err := deploy.ValidateSourceCredentialType(input.SourceAuthType, secret.Type); err != nil {
				problem(w, http.StatusBadRequest, "Invalid source credential", err.Error())
				return
			}
		}
	}
	if len(input.ComposeContent) > maxComposeContentBytes || len(input.HelmValues) > maxComposeContentBytes {
		problem(w, http.StatusRequestEntityTooLarge, "Application definition too large", "Keep Compose content and Helm values under 512 KB.")
		return
	}
	if len(input.PreDeployHook) > maxHookBytes || len(input.PostDeployHook) > maxHookBytes || strings.ContainsRune(input.PreDeployHook+input.PostDeployHook, 0) {
		problem(w, http.StatusRequestEntityTooLarge, "Deployment hook too large", "Keep each deployment hook under 64 KB and use plain shell text.")
		return
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	server, err := a.store.GetServer(r.Context(), input.ServerID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if server.State != "ready" {
		problem(w, http.StatusConflict, "Server not ready", "Choose a ready server or complete its enrollment before defining an application.")
		return
	}
	if helmApplication {
		candidate := core.App{BuildType: core.BuildTypeHelm, SourceRepo: input.SourceRepo, SourceAuthType: input.SourceAuthType,
			HelmChart: input.HelmChart, HelmRepository: input.HelmRepository}
		if err := deploy.ValidateHelmTarget(candidate, server); err != nil {
			problem(w, http.StatusBadRequest, "Helm configuration unavailable", err.Error())
			return
		}
	} else if server.Runtime != core.ServerRuntimeDocker {
		problem(w, http.StatusBadRequest, "Docker server required", "Dockerfile and Compose applications require a Docker server.")
		return
	}
	if directCompose {
		input.BuildType = string(core.BuildTypeCompose)
		input.Branch = ""
		input.ComposePath = "compose.yml"
	} else if helmApplication && input.SourceRepo == "" {
		input.Branch = ""
	} else if input.Branch == "" {
		input.Branch = "main"
	}
	if input.BuildType == "" {
		input.BuildType = string(core.BuildTypeDockerfile)
	}
	if input.BuildType != string(core.BuildTypeDockerfile) && input.BuildType != string(core.BuildTypeCompose) && input.BuildType != string(core.BuildTypeHelm) {
		problem(w, http.StatusBadRequest, "Build type unavailable", "Use dockerfile, compose, or helm.")
		return
	}
	if input.ContextPath == "" {
		input.ContextPath = "."
	}
	if input.DockerfilePath == "" {
		input.DockerfilePath = "Dockerfile"
	}
	if input.ComposePath == "" {
		input.ComposePath = "compose.yml"
	}
	state := "ready"
	if input.Template {
		state = "template"
	}
	item := core.App{ID: ulid.Make().String(), ProjectID: input.ProjectID, ServerID: input.ServerID, Name: input.Name,
		SourceRepo: input.SourceRepo, Branch: input.Branch, SourceAuthType: input.SourceAuthType, SourceCredentialID: input.SourceCredentialID,
		BuildType: core.BuildType(input.BuildType), ContextPath: input.ContextPath,
		DockerfilePath: input.DockerfilePath, ComposePath: input.ComposePath, ComposeContent: input.ComposeContent, ContainerPort: input.ContainerPort,
		HelmChart: input.HelmChart, HelmVersion: input.HelmVersion, HelmRepository: input.HelmRepository, HelmValues: input.HelmValues,
		HelmNamespace: input.HelmNamespace, HelmRelease: input.HelmRelease, PreDeployHook: input.PreDeployHook,
		PostDeployHook: input.PostDeployHook, Domain: strings.TrimSpace(input.Domain), Template: input.Template, State: state, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateApp(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type updateAppHooksRequest struct {
	PreDeployHook  string   `json:"preDeployHook"`
	PostDeployHook string   `json:"postDeployHook"`
	SecretIDs      []string `json:"secretIds"`
}

func (a *API) updateAppHooks(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.Generated {
		problem(w, http.StatusConflict, "Generated application cannot be edited", "Update hooks on its application source or event rule instead.")
		return
	}
	var input updateAppHooksRequest
	if !decode(w, r, &input) {
		return
	}
	if err := validateEventHooks(input.PreDeployHook, input.PostDeployHook); err != nil {
		problem(w, http.StatusBadRequest, "Invalid deployment hook", err.Error())
		return
	}
	if err := a.validateSecretIDs(r.Context(), input.SecretIDs); err != nil {
		problem(w, http.StatusBadRequest, "Invalid secret binding", err.Error())
		return
	}
	item.PreDeployHook = strings.TrimSpace(input.PreDeployHook)
	item.PostDeployHook = strings.TrimSpace(input.PostDeployHook)
	item.HookEnvironment = make(map[string]string, len(input.SecretIDs))
	item.HookSecretIDs = append([]string(nil), input.SecretIDs...)
	for _, id := range input.SecretIDs {
		secret, err := a.store.GetSecret(r.Context(), id)
		if err != nil {
			a.notFoundOrInternal(w, err, "Build credential")
			return
		}
		item.HookEnvironment[core.SecretEnvironmentKey(secret.ID, secret.EnvironmentVariable)] = secret.EncryptedValue
	}
	if err := a.store.UpdateApp(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type inspectHelmSourceRequest struct {
	SourceRepo         string `json:"sourceRepo"`
	Branch             string `json:"branch"`
	ChartPath          string `json:"chartPath"`
	SourceAuthType     string `json:"sourceAuthType"`
	SourceCredentialID string `json:"sourceCredentialId"`
}

func (a *API) inspectHelmSource(w http.ResponseWriter, r *http.Request) {
	var input inspectHelmSourceRequest
	if !decode(w, r, &input) {
		return
	}
	input.SourceRepo, input.Branch, input.ChartPath = deploy.NormalizeGitHelmSource(input.SourceRepo, input.Branch, input.ChartPath)
	input.SourceAuthType = strings.TrimSpace(input.SourceAuthType)
	input.SourceCredentialID = strings.TrimSpace(input.SourceCredentialID)
	input.SourceRepo = deploy.RepositoryForSourceAuth(input.SourceRepo, input.SourceAuthType)
	if input.Branch == "" {
		input.Branch = "main"
	}
	if input.SourceRepo == "" || input.ChartPath == "" {
		problem(w, http.StatusBadRequest, "Helm source required", "Enter a repository URL and chart directory, or paste a GitHub folder URL.")
		return
	}
	if detail := validateSourceAuthentication(input.SourceRepo, input.SourceAuthType, input.SourceCredentialID); detail != "" {
		problem(w, http.StatusBadRequest, "Invalid source authentication", detail)
		return
	}
	app := core.App{SourceRepo: input.SourceRepo, Branch: input.Branch, SourceAuthType: input.SourceAuthType,
		SourceCredentialID: input.SourceCredentialID, BuildType: core.BuildTypeHelm, HelmChart: input.ChartPath}
	if input.SourceCredentialID != "" {
		if a.eventConfig.Vault == nil {
			problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before inspecting private repositories.")
			return
		}
		if input.SourceAuthType == deploy.SourceAuthGitHubApp {
			connection, err := a.store.GetGitHubApp(r.Context(), input.SourceCredentialID)
			if err != nil {
				a.notFoundOrInternal(w, err, "GitHub App")
				return
			}
			if connection.State != "ready" {
				problem(w, http.StatusConflict, "GitHub App not ready", "Install and verify this GitHub App before loading the chart.")
				return
			}
			if err := githubapp.ValidateRepositoryHost(input.SourceRepo, connection.WebURL); err != nil {
				problem(w, http.StatusBadRequest, "GitHub App host mismatch", err.Error())
				return
			}
		}
		resolved, err := (deploy.SourceAuthExecutor{Secrets: a.store, Vault: a.eventConfig.Vault, Resolver: a.secretResolver, GitHubApps: a.eventConfig.GitHubApps}).Resolve(r.Context(), app)
		if err != nil {
			problem(w, http.StatusBadRequest, "Repository credential unavailable", err.Error())
			return
		}
		app = resolved
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	inspection, err := deploy.InspectGitHelmSource(ctx, app)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		problem(w, status, "Helm chart could not be loaded", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, inspection)
}

func encodeHelmValueOverrides(values map[string]interface{}) (string, error) {
	if len(values) == 0 {
		return "", nil
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("encode structured Helm values: %w", err)
	}
	if len(encoded) > maxComposeContentBytes {
		return "", errors.New("structured Helm values exceed 512 KB")
	}
	return string(encoded), nil
}

type appHelmValuesResponse struct {
	deploy.HelmChartInspection
	Overrides map[string]interface{} `json:"overrides"`
}

func (a *API) getAppHelmValues(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.BuildType != core.BuildTypeHelm {
		problem(w, http.StatusConflict, "Helm values unavailable", "This application does not deploy a Helm chart.")
		return
	}
	item.SourceRepo = deploy.RepositoryForSourceAuth(item.SourceRepo, item.SourceAuthType)
	resolved, err := (deploy.SourceAuthExecutor{Secrets: a.store, Vault: a.eventConfig.Vault, Resolver: a.secretResolver, GitHubApps: a.eventConfig.GitHubApps}).Resolve(r.Context(), item)
	if err != nil {
		problem(w, http.StatusBadRequest, "Repository credential unavailable", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	inspection, err := deploy.InspectHelmSource(ctx, resolved)
	if err != nil {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		problem(w, status, "Helm chart could not be loaded", err.Error())
		return
	}
	overrides := map[string]interface{}{}
	if strings.TrimSpace(item.HelmValues) != "" {
		if err := yaml.Unmarshal([]byte(item.HelmValues), &overrides); err != nil {
			problem(w, http.StatusUnprocessableEntity, "Saved Helm values are invalid", err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, appHelmValuesResponse{HelmChartInspection: inspection, Overrides: overrides})
}

type updateAppHelmValuesRequest struct {
	Overrides map[string]interface{} `json:"overrides"`
}

func (a *API) updateAppHelmValues(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetApp(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if item.BuildType != core.BuildTypeHelm {
		problem(w, http.StatusConflict, "Helm values unavailable", "This application does not deploy a Helm chart.")
		return
	}
	if item.Generated {
		problem(w, http.StatusConflict, "Generated application cannot be edited", "Update values on its Helm source or preview group instead.")
		return
	}
	var input updateAppHelmValuesRequest
	if !decode(w, r, &input) {
		return
	}
	encoded, err := encodeHelmValueOverrides(input.Overrides)
	if err != nil {
		problem(w, http.StatusBadRequest, "Helm values unavailable", err.Error())
		return
	}
	item.HelmValues = encoded
	if err := a.store.UpdateApp(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"overrides": input.Overrides})
}

func validateSourceAuthentication(repository, authType, credentialID string) string {
	if repository != "" {
		switch {
		case strings.HasPrefix(repository, "https://"):
			parsed, err := url.Parse(repository)
			if err != nil || parsed.Host == "" {
				return "Enter a valid HTTPS repository URL."
			}
			if parsed.User != nil {
				return "Do not embed credentials in the repository URL; attach a stored credential instead."
			}
		case strings.HasPrefix(repository, "file://"):
			if authType != "" || credentialID != "" {
				return "Local file repositories do not use stored credentials."
			}
		case strings.HasPrefix(repository, "ssh://"), strings.Contains(repository, "@"):
			if authType == "" && credentialID == "" {
				return "SSH repositories require a stored SSH private key."
			}
		default:
			return "Use an HTTPS repository URL, or SSH with a stored private key."
		}
	}
	if authType == "" && credentialID == "" {
		return ""
	}
	if repository == "" {
		return "Repository credentials require a source repository."
	}
	if authType == "" || credentialID == "" {
		return "Choose both an authentication method and a stored credential."
	}
	switch authType {
	case deploy.SourceAuthGitHubApp:
		if !strings.HasPrefix(repository, "https://") {
			return "GitHub Apps require an HTTPS repository URL."
		}
	case deploy.SourceAuthGitHubToken:
		if !strings.HasPrefix(repository, "https://") {
			return "GitHub tokens require an HTTPS repository URL."
		}
	case deploy.SourceAuthSSHKey:
		if !strings.HasPrefix(repository, "ssh://") && !strings.Contains(repository, "@") {
			return "SSH keys require an ssh:// or git@host:path repository URL."
		}
	default:
		return "Use github_app, github_token, or ssh_key."
	}
	return ""
}

func (a *API) deleteApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetApp(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	previewGroups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, group := range previewGroups {
		for _, component := range group.Components {
			if component.AppID == id {
				problem(w, http.StatusConflict, "Application is in a preview group", "Remove this application from every preview group before deleting it.")
				return
			}
		}
	}
	active, err := a.store.ActiveDeploymentForApp(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if active != nil {
		problem(w, http.StatusConflict, "Deployment active", "Wait for the active deployment to finish before deleting this application.")
		return
	}
	previews, err := a.store.ListPreviewEnvironments(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, preview := range previews {
		if preview.TemplateAppID == id && preview.State != core.PreviewClosed {
			problem(w, http.StatusConflict, "Preview active", "Close and clean every preview created from this application before deleting it.")
			return
		}
	}
	hasDeployments, err := a.store.AppHasDeployments(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	if hasDeployments {
		if err := a.deploy.Cleanup(r.Context(), id, nil); err != nil {
			switch {
			case errors.Is(err, deploy.ErrDeploymentActive):
				problem(w, http.StatusConflict, "Deployment active", "Wait for the active deployment to finish before deleting this application.")
			case errors.Is(err, deploy.ErrCleanupUnsupported):
				problem(w, http.StatusConflict, "Cleanup unavailable", "This application cannot be deleted until its deployed resources can be cleaned up safely.")
			case errors.Is(err, store.ErrNotFound):
				problem(w, http.StatusNotFound, "Application not found", "Refresh the application inventory and try again.")
			default:
				a.internal(w, err)
			}
			return
		}
	}
	if err := a.store.DeleteApp(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrAppPreviewGroup) {
			problem(w, http.StatusConflict, "Application is in a preview group", "Remove this application from every preview group before deleting it.")
			return
		}
		if errors.Is(err, store.ErrAppActive) {
			problem(w, http.StatusConflict, "Deployment active", "A deployment started while the application was being deleted. Wait for it to finish and retry.")
			return
		}
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) cleanupApp(w http.ResponseWriter, r *http.Request) {
	err := a.deploy.Cleanup(r.Context(), chi.URLParam(r, "id"), nil)
	if errors.Is(err, deploy.ErrDeploymentActive) {
		problem(w, http.StatusConflict, "Deployment active", "Wait for the active deployment to finish before cleaning up the application.")
		return
	}
	if errors.Is(err, deploy.ErrCleanupUnsupported) {
		problem(w, http.StatusConflict, "Cleanup unavailable", err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Application not found", "Refresh the application inventory and try again.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) startDeployment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CommitSHA string `json:"commitSha"`
	}
	if r.ContentLength > 0 && !decode(w, r, &input) {
		return
	}
	item, err := a.deploy.Start(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(input.CommitSHA))
	if errors.Is(err, deploy.ErrApplicationTemplate) {
		problem(w, http.StatusConflict, "Template cannot be deployed", "Use this template from an event rule or preview group.")
		return
	}
	if errors.Is(err, deploy.ErrDeploymentActive) {
		problem(w, http.StatusConflict, "Deployment already active", err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Application not found", "Refresh the application inventory and try again.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (a *API) getDeployment(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Deployment not found", "The deployment record does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) getDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	items, err := a.store.ListDeploymentLogs(r.Context(), chi.URLParam(r, "id"), after)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	if err := a.deploy.Cancel(r.Context(), chi.URLParam(r, "id")); err != nil {
		problem(w, http.StatusConflict, "Deployment cannot be cancelled", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deploymentEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, http.StatusInternalServerError, "Streaming unavailable", "The HTTP server does not support streaming.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	lastID := int64(0)
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		logs, err := a.store.ListDeploymentLogs(r.Context(), chi.URLParam(r, "id"), lastID)
		if err != nil {
			return
		}
		for _, entry := range logs {
			lastID = entry.ID
			payload, _ := json.Marshal(entry)
			fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", entry.ID, payload)
		}
		deployment, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			return
		}
		payload, _ := json.Marshal(deployment)
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", payload)
		flusher.Flush()
		if deployment.State.Terminal() && len(logs) == 0 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) providerContract(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": provider.APIVersion, "transport": "HTTP/JSON sidecar", "operations": []string{"manifest", "validate", "options", "createServer", "operation", "server", "deleteServer"}})
}

func (a *API) runtimeContract(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": "dispatch.runtime/v1", "enabled": []string{core.ServerRuntimeDocker, core.ServerRuntimeKubernetes, core.ServerRuntimeOpenShift}, "planned": []string{}, "operations": []string{"deploy", "inspect", "logs", "start", "stop", "rollback", "destroy"}})
}

func (a *API) notFoundOrInternal(w http.ResponseWriter, err error, resource string) {
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, resource+" not found", "Refresh the inventory and try again.")
		return
	}
	a.internal(w, err)
}

func (a *API) internal(w http.ResponseWriter, err error) {
	a.logger.Error("api request failed", "error", err)
	problem(w, http.StatusInternalServerError, "Request failed", "Dispatch could not complete the request. Check the controller log and retry.")
}

func (a *API) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 10<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		problem(w, http.StatusBadRequest, "Invalid request", "Send valid JSON containing only supported fields.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func problem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "title": title, "detail": detail})
}

func Shutdown(ctx context.Context, server *http.Server) error { return server.Shutdown(ctx) }

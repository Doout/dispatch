package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/groups"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/openshift"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/ui"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
)

type AuthConfig struct {
	AdminToken string
	Username   string
	Password   string
}

type EventConfig struct {
	WebhookSecret  string
	DefaultCommand string
	GitHubAPIURL   string
	GitHubToken    string
}

type API struct {
	store       store.Store
	deploy      *deploy.Service
	demo        bool
	auth        AuthConfig
	logger      *slog.Logger
	events      *events.Service
	groups      *groups.Service
	eventConfig EventConfig
	openShift   *openshift.Bootstrapper

	sessionMu sync.RWMutex
	sessions  map[string]time.Time
}

func New(data store.Store, deployments *deploy.Service, demo bool, auth AuthConfig, logger *slog.Logger, eventConfigs ...EventConfig) http.Handler {
	eventConfig := EventConfig{DefaultCommand: "/preview"}
	if len(eventConfigs) > 0 {
		eventConfig = eventConfigs[0]
		if eventConfig.DefaultCommand == "" {
			eventConfig.DefaultCommand = "/preview"
		}
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
		}(), nil), eventConfig: eventConfig, openShift: openshift.New()}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Use(a.logRequest)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/auth/status", a.authStatus)
		r.Post("/auth/setup", a.setupAdmin)
		r.Post("/auth/login", a.login)
		r.Post("/events/github", a.githubWebhook)
		r.Group(func(r chi.Router) {
			r.Use(a.authorize)
			r.Get("/overview", a.overview)
			r.Get("/projects", a.listProjects)
			r.Post("/projects", a.createProject)
			r.Put("/projects/{id}", a.updateProject)
			r.Delete("/projects/{id}", a.deleteProject)
			r.Get("/servers", a.listServers)
			r.Post("/servers", a.createServer)
			r.Put("/servers/{id}", a.updateServer)
			r.Post("/servers/{id}/repair", a.repairOpenShiftServer)
			r.Delete("/servers/{id}", a.deleteServer)
			r.Get("/apps", a.listApps)
			r.Post("/apps", a.createApp)
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
			r.Get("/deployments/{id}/logs", a.getDeploymentLogs)
			r.Post("/deployments/{id}/cancel", a.cancelDeployment)
			r.Get("/deployments/{id}/events", a.deploymentEvents)
			r.Post("/apps/{id}/deployments", a.startDeployment)
			r.Get("/contracts/provider", a.providerContract)
			r.Get("/contracts/runtime", a.runtimeContract)
		})
	})
	r.Handle("/*", ui.Handler())
	r.Handle("/", ui.Handler())
	return r
}

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
	return found && time.Now().Before(expires)
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
	token, err := a.createSession()
	if err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func (a *API) createSession() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buffer)
	now := time.Now()
	a.sessionMu.Lock()
	for existing, expires := range a.sessions {
		if now.After(expires) {
			delete(a.sessions, existing)
		}
	}
	a.sessions[token] = now.Add(12 * time.Hour)
	a.sessionMu.Unlock()
	return token, nil
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
	writeJSON(w, http.StatusOK, core.Overview{Demo: a.demo, Projects: projects, Servers: servers, Apps: apps, Deployments: deployments,
		EventTriggers: eventTriggers, Previews: previews, PreviewGroups: previewGroups, PreviewGroupRuns: previewGroupRuns})
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
	Provider       string `json:"provider"`
	Repository     string `json:"repository"`
	Command        string `json:"command"`
	Enabled        *bool  `json:"enabled"`
	PreDeployHook  string `json:"preDeployHook"`
	PostDeployHook string `json:"postDeployHook"`
}

const maxEventHookBytes = 64 << 10

func validateEventHooks(preDeployHook, postDeployHook string) error {
	if len(preDeployHook) > maxEventHookBytes || len(postDeployHook) > maxEventHookBytes {
		return fmt.Errorf("each deployment hook must be no larger than 64 KiB")
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
	requested := core.EventTrigger{ID: ulid.Make().String(), AppID: appID, Provider: core.EventProvider(providerName),
		Repository: repository, Command: command, Enabled: enabled, PreDeployHook: input.PreDeployHook,
		PostDeployHook: input.PostDeployHook, CreatedAt: now, UpdatedAt: now}
	item, created, err := a.store.CreateEventTrigger(r.Context(), requested)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !created && (item.Command != requested.Command || item.Enabled != requested.Enabled || item.PreDeployHook != requested.PreDeployHook || item.PostDeployHook != requested.PostDeployHook) {
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
	command, err := events.NormalizeCommand(input.Command, item.Command)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid trigger command", err.Error())
		return
	}
	enabled := item.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	item.Command = command
	item.Enabled = enabled
	item.PreDeployHook = input.PreDeployHook
	item.PostDeployHook = input.PostDeployHook
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid webhook body", "Keep the webhook payload under 1 MB.")
		return
	}
	if err := events.VerifySignature(a.eventConfig.WebhookSecret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		problem(w, http.StatusUnauthorized, "Invalid webhook signature", "Sign the request body with the configured webhook secret.")
		return
	}
	event, err := events.ParseGitHubEvent(r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery"), body, time.Now().UTC())
	if errors.Is(err, events.ErrEventUnsupported) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid webhook event", err.Error())
		return
	}
	groupRuns, err := a.groups.Process(r.Context(), event)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "Preview group event rejected", err.Error())
		return
	}
	result, err := a.events.Process(r.Context(), event)
	if err != nil {
		a.internal(w, err)
		return
	}
	result.PreviewGroupRuns = groupRuns
	status := http.StatusAccepted
	if result.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
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
	default:
		problem(w, http.StatusBadRequest, "Runtime unavailable", "Use docker, kubernetes, or openshift.")
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
	default:
		return strings.ToLower(strings.TrimSpace(runtime))
	}
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
	if err := a.store.DeleteServer(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type createAppRequest struct {
	ProjectID, ServerID, Name, SourceRepo, Branch, BuildType, ContextPath, DockerfilePath, ComposePath, ComposeContent, Domain string
	HelmChart, HelmVersion, HelmRepository, HelmValues, HelmNamespace, HelmRelease                                             string
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
	if input.ProjectID == "" || input.ServerID == "" || input.Name == "" {
		problem(w, http.StatusBadRequest, "Application details required", "Choose a project and server, then enter an application name.")
		return
	}
	if input.SourceRepo == "" && !directCompose && !helmApplication {
		problem(w, http.StatusBadRequest, "Application source required", "Enter a repository URL, paste a Docker Compose file, or configure a Helm chart.")
		return
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
		candidate := core.App{BuildType: core.BuildTypeHelm, HelmChart: input.HelmChart, HelmRepository: input.HelmRepository}
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
	} else if helmApplication {
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
		SourceRepo: input.SourceRepo, Branch: input.Branch, BuildType: core.BuildType(input.BuildType), ContextPath: input.ContextPath,
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

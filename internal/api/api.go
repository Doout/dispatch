package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/ui"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/oklog/ulid/v2"
)

type API struct {
	store      store.Store
	deploy     *deploy.Service
	demo       bool
	adminToken string
	logger     *slog.Logger
}

func New(data store.Store, deployments *deploy.Service, demo bool, adminToken string, logger *slog.Logger) http.Handler {
	a := &API{store: data, deploy: deployments, demo: demo, adminToken: adminToken, logger: logger}
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Use(a.logRequest)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(a.authorize)
		r.Get("/overview", a.overview)
		r.Get("/projects", a.listProjects)
		r.Post("/projects", a.createProject)
		r.Get("/servers", a.listServers)
		r.Post("/servers", a.createServer)
		r.Get("/apps", a.listApps)
		r.Post("/apps", a.createApp)
		r.Get("/deployments", a.listDeployments)
		r.Get("/deployments/{id}", a.getDeployment)
		r.Get("/deployments/{id}/logs", a.getDeploymentLogs)
		r.Post("/deployments/{id}/cancel", a.cancelDeployment)
		r.Get("/deployments/{id}/events", a.deploymentEvents)
		r.Post("/apps/{id}/deployments", a.startDeployment)
		r.Get("/contracts/provider", a.providerContract)
		r.Get("/contracts/runtime", a.runtimeContract)
	})
	r.Handle("/*", ui.Handler())
	r.Handle("/", ui.Handler())
	return r
}

func (a *API) authorize(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.adminToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(a.adminToken)) != 1 {
			problem(w, http.StatusUnauthorized, "Authentication required", "Provide the configured Dispatch administrator token.")
			return
		}
		next.ServeHTTP(w, r)
	})
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
	writeJSON(w, http.StatusOK, core.Overview{Demo: a.demo, Projects: projects, Servers: servers, Apps: apps, Deployments: deployments})
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

type createServerRequest struct{ Name, Address, Runtime, AgentMode string }

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	var input createServerRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.Address = strings.TrimSpace(input.Name), strings.TrimSpace(input.Address)
	if input.Name == "" || input.Address == "" {
		problem(w, http.StatusBadRequest, "Server details required", "Enter both a server name and address.")
		return
	}
	if input.Runtime == "" {
		input.Runtime = "docker"
	}
	if input.Runtime != "docker" {
		problem(w, http.StatusBadRequest, "Runtime unavailable", "Docker is the only runtime enabled in this milestone.")
		return
	}
	if input.AgentMode == "" {
		input.AgentMode = "ssh-bootstrap"
	}
	item := core.Server{ID: ulid.Make().String(), Name: input.Name, Address: input.Address, Runtime: input.Runtime, State: "pending", AgentMode: input.AgentMode, CreatedAt: time.Now().UTC()}
	if input.Address == "local" {
		item.State = "ready"
	}
	if err := a.store.CreateServer(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type createAppRequest struct {
	ProjectID, ServerID, Name, SourceRepo, Branch, BuildType, ContextPath, DockerfilePath, ComposePath, Domain string
	ContainerPort                                                                                              int
}

func (a *API) createApp(w http.ResponseWriter, r *http.Request) {
	var input createAppRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.ProjectID == "" || input.ServerID == "" || input.Name == "" || input.SourceRepo == "" {
		problem(w, http.StatusBadRequest, "Application details required", "Choose a project and server, then enter a name and source repository.")
		return
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		a.notFoundOrInternal(w, err, "Project")
		return
	}
	if _, err := a.store.GetServer(r.Context(), input.ServerID); err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	if input.Branch == "" {
		input.Branch = "main"
	}
	if input.BuildType == "" {
		input.BuildType = string(core.BuildTypeDockerfile)
	}
	if input.BuildType != string(core.BuildTypeDockerfile) && input.BuildType != string(core.BuildTypeCompose) {
		problem(w, http.StatusBadRequest, "Build type unavailable", "Use dockerfile or compose.")
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
	item := core.App{ID: ulid.Make().String(), ProjectID: input.ProjectID, ServerID: input.ServerID, Name: input.Name,
		SourceRepo: strings.TrimSpace(input.SourceRepo), Branch: input.Branch, BuildType: core.BuildType(input.BuildType), ContextPath: input.ContextPath,
		DockerfilePath: input.DockerfilePath, ComposePath: input.ComposePath, ContainerPort: input.ContainerPort,
		Domain: strings.TrimSpace(input.Domain), State: "ready", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateApp(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) startDeployment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CommitSHA string `json:"commitSha"`
	}
	if r.ContentLength > 0 && !decode(w, r, &input) {
		return
	}
	item, err := a.deploy.Start(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(input.CommitSHA))
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
	writeJSON(w, http.StatusOK, map[string]any{"apiVersion": "dispatch.runtime/v1", "enabled": []string{"docker"}, "planned": []string{"k3s", "kubernetes"}, "operations": []string{"deploy", "inspect", "logs", "start", "stop", "rollback", "destroy"}})
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
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
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

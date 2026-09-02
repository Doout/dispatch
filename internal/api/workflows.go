package api

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	workflowservice "github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type configSourceRequest struct {
	ProjectID           string `json:"projectId"`
	GitHubAppID         string `json:"githubAppId"`
	CredentialSecretID  string `json:"credentialSecretId"`
	Name                string `json:"name"`
	Repository          string `json:"repository"`
	Branch              string `json:"branch"`
	Path                string `json:"path"`
	SyncMode            string `json:"syncMode"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds"`
}

func (a *API) listConfigSources(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListConfigSources(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	filtered := items[:0]
	member := currentIdentity(r.Context()).SystemRole != core.UserRoleOwner
	for _, item := range items {
		if visible[item.ProjectID] {
			if member {
				item = redactConfigSourceCredentials(item)
			}
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (a *API) createConfigSource(w http.ResponseWriter, r *http.Request) {
	var input configSourceRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, strings.TrimSpace(input.ProjectID)) {
		return
	}
	item, detail := a.configSourceFromRequest(r, core.ConfigSource{ID: ulid.Make().String(), CreatedAt: time.Now().UTC()}, input)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Configuration source invalid", detail)
		return
	}
	if err := a.store.CreateConfigSource(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	item, syncErr := a.workflows.SyncSource(r.Context(), item.ID)
	if syncErr != nil {
		// The source remains visible with its precise validation or connection
		// error so it can be corrected without re-entering every field.
		writeJSON(w, http.StatusCreated, item)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateConfigSource(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetConfigSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	var input configSourceRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.requireProject(w, r, core.PermissionProjectConfigure, strings.TrimSpace(input.ProjectID)) {
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		input.GitHubAppID = item.GitHubAppID
		input.CredentialSecretID = item.CredentialSecretID
	}
	item, detail := a.configSourceFromRequest(r, item, input)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Configuration source invalid", detail)
		return
	}
	if err := a.store.UpdateConfigSource(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	item, _ = a.workflows.SyncSource(r.Context(), item.ID)
	writeJSON(w, http.StatusOK, item)
}

func (a *API) configSourceFromRequest(r *http.Request, item core.ConfigSource, input configSourceRequest) (core.ConfigSource, string) {
	input.ProjectID, input.GitHubAppID, input.CredentialSecretID, input.Name = strings.TrimSpace(input.ProjectID), strings.TrimSpace(input.GitHubAppID), strings.TrimSpace(input.CredentialSecretID), strings.TrimSpace(input.Name)
	input.Repository = strings.Trim(strings.TrimSpace(input.Repository), "/")
	input.Branch, input.Path, input.SyncMode = strings.TrimSpace(input.Branch), strings.Trim(strings.TrimSpace(input.Path), "/"), strings.TrimSpace(input.SyncMode)
	if input.ProjectID == "" || input.Name == "" || input.Repository == "" {
		return item, "Choose a project and repository access, then enter a name and repository."
	}
	if (input.GitHubAppID == "") == (input.CredentialSecretID == "") {
		return item, "Choose one GitHub App or saved repository credential."
	}
	if input.GitHubAppID != "" {
		input.Repository = strings.TrimSuffix(input.Repository, ".git")
		if strings.Contains(input.Repository, "://") {
			parts := strings.SplitN(input.Repository, "://", 2)
			path := strings.SplitN(parts[1], "/", 2)
			if len(path) != 2 {
				return item, "Repository must use owner/name."
			}
			input.Repository = strings.TrimSuffix(strings.Trim(path[1], "/"), ".git")
		}
		if len(strings.Split(input.Repository, "/")) != 2 {
			return item, "Repository must use owner/name."
		}
	} else if strings.ContainsAny(input.Repository, " \t\r\n") || (!strings.Contains(input.Repository, "://") && !strings.Contains(input.Repository, "@")) {
		return item, "Repository credentials require an HTTPS or SSH clone URL."
	}
	if input.Branch == "" {
		input.Branch = "main"
	}
	if input.Path == "" {
		input.Path = ".dispatch"
	}
	if input.SyncMode == "" {
		input.SyncMode = core.ConfigSyncWebhookPoll
	}
	if input.SyncMode != core.ConfigSyncWebhookPoll && input.SyncMode != core.ConfigSyncWebhook && input.SyncMode != core.ConfigSyncPoll {
		return item, "Sync mode must be webhook_poll, webhook, or poll."
	}
	if input.PollIntervalSeconds == 0 {
		input.PollIntervalSeconds = 300
	}
	if input.PollIntervalSeconds < 30 || input.PollIntervalSeconds > 86400 {
		return item, "Poll interval must be between 30 seconds and 24 hours."
	}
	if _, err := a.store.GetProject(r.Context(), input.ProjectID); err != nil {
		return item, "Choose an existing project."
	}
	if input.GitHubAppID != "" {
		connection, err := a.store.GetGitHubApp(r.Context(), input.GitHubAppID)
		if err != nil || connection.State != "ready" {
			return item, "Choose a ready GitHub App connection."
		}
		if err := a.validateGitHubRepositoryAccess(r.Context(), input.GitHubAppID, input.Repository); err != nil {
			return item, err.Error()
		}
	} else {
		if input.SyncMode != core.ConfigSyncPoll {
			return item, "Saved repository credentials support polling only."
		}
		secret, err := a.store.GetSecret(r.Context(), input.CredentialSecretID)
		if err != nil {
			return item, "Choose an existing repository credential."
		}
		authType := deploy.SourceAuthGitHubToken
		if secret.Type == core.SecretTypeSSHPrivateKey {
			authType = deploy.SourceAuthSSHKey
		}
		if err := deploy.ValidateSourceCredentialType(authType, secret.Type); err != nil {
			return item, err.Error()
		}
	}
	now := time.Now().UTC()
	item.ProjectID, item.GitHubAppID, item.CredentialSecretID, item.Name, item.Repository = input.ProjectID, input.GitHubAppID, input.CredentialSecretID, input.Name, input.Repository
	item.Branch, item.Path, item.SyncMode, item.PollIntervalSeconds = input.Branch, input.Path, input.SyncMode, input.PollIntervalSeconds
	item.Active, item.State, item.LastError, item.UpdatedAt = true, "syncing", "", now
	return item, ""
}

func (a *API) syncConfigSource(w http.ResponseWriter, r *http.Request) {
	item, err := a.workflows.SyncSource(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, item)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteConfigSource(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteConfigSource(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listWorkflowResources(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkflowResources(r.Context(), strings.TrimSpace(r.URL.Query().Get("configSourceId")))
	if err != nil {
		a.internal(w, err)
		return
	}
	visibleProjects, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	sources, err := a.store.ListConfigSources(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	visibleSources := map[string]bool{}
	for _, source := range sources {
		if visibleProjects[source.ProjectID] {
			visibleSources[source.ID] = true
		}
	}
	visibleItems := items[:0]
	for _, item := range items {
		if visibleSources[item.ConfigSourceID] {
			visibleItems = append(visibleItems, item)
		}
	}
	items = visibleItems
	if err == nil && r.URL.Query().Get("includeRemoved") != "true" {
		current := items[:0]
		for _, item := range items {
			if item.State != "removed" {
				current = append(current, item)
			}
		}
		items = current
	}
	a.list(w, items, err)
}

func (a *API) getWorkflowTopology(w http.ResponseWriter, r *http.Request) {
	resource, err := a.store.GetWorkflowResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow resource")
		return
	}
	topology, err := workflowservice.BuildTopology(resource.Path, []byte(resource.Document))
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "Topology unavailable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, topology)
}

func (a *API) activateWorkflowResource(w http.ResponseWriter, r *http.Request) {
	resource, revision, err := a.workflows.Activate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.workflowProblem(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"resource": resource, "revision": revision})
}

func (a *API) deactivateWorkflowResource(w http.ResponseWriter, r *http.Request) {
	resource, err := a.workflows.Deactivate(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.workflowProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resource)
}

func (a *API) runWorkflowResource(w http.ResponseWriter, r *http.Request) {
	revision, err := a.workflows.Start(r.Context(), chi.URLParam(r, "id"), "manual")
	if err != nil {
		a.workflowProblem(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, revision)
}

func (a *API) listWorkflowRevisions(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkflowRevisions(r.Context(), strings.TrimSpace(r.URL.Query().Get("resourceId")), 100)
	if err != nil {
		a.internal(w, err)
		return
	}
	visibleProjects, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	sources, err := a.store.ListConfigSources(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	visibleSources := map[string]bool{}
	for _, source := range sources {
		if visibleProjects[source.ProjectID] {
			visibleSources[source.ID] = true
		}
	}
	resources, err := a.store.ListWorkflowResources(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	visibleResources := map[string]bool{}
	for _, resource := range resources {
		if visibleSources[resource.ConfigSourceID] {
			visibleResources[resource.ID] = true
		}
	}
	filtered := items[:0]
	for _, item := range items {
		if visibleResources[item.ResourceID] {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (a *API) getWorkflowRevision(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetWorkflowRevision(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow revision")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) listWorkflowJobs(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkflowJobResults(r.Context(), chi.URLParam(r, "id"))
	a.list(w, items, err)
}

func (a *API) listWorkflowStages(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkflowStageRuns(r.Context(), chi.URLParam(r, "id"))
	a.list(w, items, err)
}

func (a *API) approveWorkflowStage(w http.ResponseWriter, r *http.Request) {
	item, err := a.workflows.ApproveStage(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.workflowProblem(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (a *API) validateWorkflowDocument(w http.ResponseWriter, r *http.Request) {
	contents, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		problem(w, http.StatusRequestEntityTooLarge, "Configuration too large", "Keep the document under 1 MiB.")
		return
	}
	documents, err := workflowservice.Parse("request", contents)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "Configuration invalid", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, documents)
}

func (a *API) workflowProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Workflow resource not found", "Refresh the page and try again.")
		return
	}
	problem(w, http.StatusUnprocessableEntity, "Workflow action failed", err.Error())
}

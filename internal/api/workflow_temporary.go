package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	pathpkg "path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	workflowservice "github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type temporaryWorkflowRequest struct {
	ConfigSourceID string `json:"configSourceId"`
	Document       string `json:"document"`
	PreviewID      string `json:"previewId"`
	Run            bool   `json:"run"`
}

type workflowPreviewImportRequest struct {
	GitHubAppID       string `json:"githubAppId"`
	Repository        string `json:"repository"`
	PullRequestNumber int    `json:"pullRequestNumber"`
	Path              string `json:"path"`
}

// Load one Application file from the current head of a PR into the editor.
// Saving still stores a snapshot in Dispatch; a later PR commit does not
// silently change an existing preview's deployment definition.
func (a *API) importWorkflowPreviewDocument(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.GitHubApps == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App unavailable", "Configure a GitHub App before loading a preview file.")
		return
	}
	var input workflowPreviewImportRequest
	if !decode(w, r, &input) {
		return
	}
	input.GitHubAppID = strings.TrimSpace(input.GitHubAppID)
	input.Repository = events.NormalizeRepository(input.Repository)
	input.Path = strings.TrimSpace(input.Path)
	extension := strings.ToLower(pathpkg.Ext(input.Path))
	if input.GitHubAppID == "" || input.Repository == "" || input.PullRequestNumber < 1 || len(input.Path) > 512 || input.Path == "." || pathpkg.IsAbs(input.Path) || pathpkg.Clean(input.Path) != input.Path || strings.HasPrefix(input.Path, "../") || extension != ".yaml" && extension != ".yml" && extension != ".json" {
		problem(w, http.StatusBadRequest, "Preview file path invalid", "Provide an installed GitHub App, PR, and relative YAML or JSON file path.")
		return
	}
	connection, err := a.store.GetGitHubApp(r.Context(), input.GitHubAppID)
	if err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return
	}
	if connection.State != "ready" || connection.InstallationID < 1 {
		problem(w, http.StatusConflict, "GitHub App unavailable", "Choose an installed GitHub App.")
		return
	}
	resolver := events.GitHubResolver{BaseURL: connection.APIURL, RepositoryTokenSource: func(ctx context.Context, repository string) (string, error) {
		return a.eventConfig.GitHubApps.RepositoryToken(ctx, input.GitHubAppID, repository)
	}}
	pr, err := resolver.ResolvePullRequest(r.Context(), input.Repository, input.PullRequestNumber)
	if err != nil {
		problem(w, http.StatusBadGateway, "Pull request lookup failed", err.Error())
		return
	}
	if !pr.Open {
		problem(w, http.StatusConflict, "Pull request closed", "Choose an open pull request.")
		return
	}
	files, err := a.eventConfig.GitHubApps.RepositoryFiles(r.Context(), input.GitHubAppID, input.Repository, pr.HeadSHA, input.Path)
	if errors.Is(err, githubapp.ErrNoConfigurationFiles) {
		problem(w, http.StatusNotFound, "Preview file not found", err.Error())
		return
	}
	if err != nil {
		problem(w, http.StatusBadGateway, "Preview file load failed", err.Error())
		return
	}
	if len(files) != 1 || files[0].Path != input.Path {
		problem(w, http.StatusUnprocessableEntity, "Preview file path invalid", "Choose one YAML or JSON file, not a directory.")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Document string `json:"document"`
		Path     string `json:"path"`
		HeadSHA  string `json:"headSha"`
	}{Document: string(files[0].Contents), Path: files[0].Path, HeadSHA: pr.HeadSHA})
}

func (a *API) createTemporaryWorkflowResource(w http.ResponseWriter, r *http.Request) {
	var input temporaryWorkflowRequest
	if !decode(w, r, &input) {
		return
	}
	input.ConfigSourceID = strings.TrimSpace(input.ConfigSourceID)
	if input.ConfigSourceID == "" || strings.TrimSpace(input.Document) == "" {
		problem(w, http.StatusBadRequest, "Temporary workflow invalid", "configSourceId and document are required.")
		return
	}
	if len(input.Document) > 1<<20 {
		problem(w, http.StatusRequestEntityTooLarge, "Configuration too large", "Keep the document under 1 MiB.")
		return
	}
	if input.PreviewID != "" {
		a.temporaryPreviewMu.Lock()
		defer a.temporaryPreviewMu.Unlock()
		existing, err := a.store.ListWorkflowResources(r.Context(), "")
		if err != nil {
			a.internal(w, err)
			return
		}
		input.Document, err = renderTemporaryPreviewID(input.Document, input.PreviewID, existing)
		if err != nil {
			problem(w, http.StatusBadRequest, "Temporary preview ID invalid", err.Error())
			return
		}
	}
	resource, err := a.workflows.CreateTemporaryApplication(r.Context(), input.ConfigSourceID, []byte(input.Document))
	if err != nil {
		a.workflowProblem(w, err)
		return
	}
	if !input.Run {
		writeJSON(w, http.StatusCreated, resource)
		return
	}
	revision, err := a.workflows.Start(r.Context(), resource.ID, "temporary preview")
	if err != nil {
		resource.Active, resource.State, resource.LastError = false, "invalid", err.Error()
		_ = a.store.UpdateWorkflowResource(r.Context(), resource)
		a.workflowProblem(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Resource core.WorkflowResource `json:"resource"`
		Revision core.WorkflowRevision `json:"revision"`
	}{resource, revision})
}

func (a *API) updateTemporaryWorkflowResource(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Document string `json:"document"`
	}
	if !decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Document) == "" || len(input.Document) > 1<<20 {
		problem(w, http.StatusBadRequest, "Temporary workflow invalid", "Provide one complete Application document under 1 MiB.")
		return
	}
	resource, err := a.workflows.UpdateTemporaryApplication(r.Context(), chi.URLParam(r, "id"), []byte(input.Document))
	if err != nil {
		a.workflowProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resource)
}

func (a *API) deleteTemporaryWorkflowResource(w http.ResponseWriter, r *http.Request) {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	resource, err := a.store.GetWorkflowResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Temporary workflow resource")
		return
	}
	if !resource.Temporary {
		problem(w, http.StatusConflict, "PR preview required", "Only a temporary Application can be deleted here.")
		return
	}
	if resource.State == "removed" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	activeRun := func() (bool, error) {
		revisions, err := a.store.ListWorkflowRevisions(r.Context(), resource.ID, 0)
		if err != nil {
			return false, err
		}
		for _, revision := range revisions {
			if revision.State == "queued" || revision.State == "running" || revision.State == "awaiting_approval" {
				return true, nil
			}
		}
		return false, nil
	}
	active, err := activeRun()
	if err != nil {
		a.internal(w, err)
		return
	}
	if active {
		problem(w, http.StatusConflict, "Preview run in progress", "Wait for the current workflow run to finish before deleting this preview.")
		return
	}
	if _, err := a.workflows.Deactivate(r.Context(), resource.ID); err != nil {
		a.workflowProblem(w, err)
		return
	}
	active, err = activeRun()
	if err != nil {
		a.internal(w, err)
		return
	}
	if active {
		problem(w, http.StatusConflict, "Preview run in progress", "The preview is paused. Wait for its current run to finish, then delete it.")
		return
	}
	if err := a.cleanupWorkflowPreviewResource(r.Context(), resource); err != nil {
		a.internal(w, err)
		return
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.ResourceID == resource.ID && trigger.ClosedAt == nil {
			if err := a.store.CloseWorkflowPreviewTrigger(r.Context(), trigger.ID, time.Now().UTC()); err != nil {
				a.internal(w, err)
				return
			}
		}
	}
	resource.Active, resource.State, resource.UpdatedAt = false, "removed", time.Now().UTC()
	if err := a.store.UpdateWorkflowResource(r.Context(), resource); err != nil {
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Preview links are derived from the GitHub App host so enterprise PRs point
// to their actual GitHub instance. Only resources already visible to the caller
// are enriched.
func (a *API) enrichWorkflowPreviewPullRequests(ctx context.Context, resources []core.WorkflowResource) error {
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	apps, err := a.store.ListGitHubApps(ctx)
	if err != nil {
		return err
	}
	githubHosts := map[string]string{}
	for _, app := range apps {
		githubHosts[app.ID] = strings.TrimRight(app.WebURL, "/")
	}
	byResource := map[string]core.WorkflowPreviewTrigger{}
	for _, trigger := range triggers {
		previous, exists := byResource[trigger.ResourceID]
		if !exists || previous.ClosedAt != nil || trigger.ClosedAt == nil {
			byResource[trigger.ResourceID] = trigger
		}
	}
	for index := range resources {
		resource := &resources[index]
		if !resource.Temporary {
			continue
		}
		trigger, ok := byResource[resource.ID]
		if !ok {
			continue
		}
		base := githubHosts[trigger.GitHubAppID]
		if base == "" {
			continue
		}
		pullRequest := func(repository string, number int) core.HelmPullRequest {
			repository = events.NormalizeRepository(repository)
			return core.HelmPullRequest{Repository: repository, Number: number, URL: base + "/" + repository + "/pull/" + strconv.Itoa(number)}
		}
		resource.PreviewPullRequests = []core.HelmPullRequest{pullRequest(trigger.Repository, trigger.PullRequestNumber)}
		documents, err := workflowservice.Parse(resource.Path, []byte(resource.Document))
		if err != nil || len(documents) != 1 || documents[0].Spec == nil {
			continue
		}
		aliases := make([]string, 0, len(trigger.LinkedPullRequests))
		for alias := range trigger.LinkedPullRequests {
			aliases = append(aliases, alias)
		}
		slices.Sort(aliases)
		for _, alias := range aliases {
			source, ok := documents[0].Spec.Sources[alias]
			if ok && trigger.LinkedPullRequests[alias] > 0 {
				resource.PreviewPullRequests = append(resource.PreviewPullRequests, pullRequest(source.Repository, trigger.LinkedPullRequests[alias]))
			}
		}
	}
	return nil
}

const temporaryPreviewPlaceholder = "__PREVIEW_ID__" // Legacy saved templates.

// A preview ID appears wherever the caller needs an isolated identity: in the
// Application name, Helm release, workload names, and labels. A collision gets
// a random suffix while the pull request number remains visible.
func renderTemporaryPreviewID(template, preferred string, existing []core.WorkflowResource) (string, error) {
	rendered, _, err := allocateTemporaryPreviewID(template, preferred, existing)
	return rendered, err
}

func allocateTemporaryPreviewID(template, preferred string, existing []core.WorkflowResource, contexts ...workflowservice.TemplateVariables) (string, string, error) {
	preferred = strings.TrimSpace(preferred)
	if preferred == "" || len(preferred) > 32 || strings.Trim(preferred, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" ||
		strings.HasPrefix(preferred, "-") || strings.HasSuffix(preferred, "-") || !workflowservice.HasInstanceID(template) {
		return "", "", errors.New("use a lowercase previewId of up to 32 letters, digits, or hyphens and place {{ instance.id }} in the document")
	}
	variables := workflowservice.TemplateVariables{}
	variables.PRNumber, _ = strconv.Atoi(preferred)
	if len(contexts) > 0 {
		variables = contexts[0]
	}
	for attempt := 0; attempt < 16; attempt++ {
		candidate := preferred
		if attempt > 0 {
			candidate += "-" + strings.ToLower(ulid.Make().String()[20:])
		}
		variables.ID = candidate
		contents, err := workflowservice.RenderWorkflowTemplate([]byte(template), variables)
		if err != nil {
			return "", "", err
		}
		rendered := string(contents)
		documents, err := workflowservice.Parse("temporary.yaml", []byte(rendered))
		if err != nil {
			return "", "", err
		}
		if len(documents) != 1 || documents[0].Spec == nil {
			return "", "", errors.New("provide exactly one Application document")
		}
		if !temporaryPreviewCollides(documents[0], existing) {
			return rendered, candidate, nil
		}
	}
	return "", "", errors.New("could not allocate a unique preview ID")
}

func temporaryPreviewCollides(document workflowservice.Document, existing []core.WorkflowResource) bool {
	for _, resource := range existing {
		if resource.State == "removed" {
			continue
		}
		if resource.Name == document.Metadata.Name {
			return true
		}
		parsed, err := workflowservice.Parse(resource.Path, []byte(resource.Document))
		if err != nil || len(parsed) != 1 || parsed[0].Spec == nil {
			continue
		}
		for _, wanted := range document.Spec.Deployments {
			for _, deployed := range parsed[0].Spec.Deployments {
				if wanted.Helm.ReleaseName != "" && wanted.Helm.ReleaseName == deployed.Helm.ReleaseName &&
					wanted.Helm.Namespace == deployed.Helm.Namespace {
					return true
				}
			}
		}
	}
	return false
}

type workflowPreviewTriggerRequest struct {
	GitHubAppID       string `json:"githubAppId"`
	Repository        string `json:"repository"`
	PullRequestNumber int    `json:"pullRequestNumber"`
	Command           string `json:"command"`
	PreviewURL        string `json:"previewUrl"`
}

func validWorkflowPreviewURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func (a *API) validateWorkflowPreviewTrigger(w http.ResponseWriter, r *http.Request, resource core.WorkflowResource, input *workflowPreviewTriggerRequest) bool {
	input.GitHubAppID = strings.TrimSpace(input.GitHubAppID)
	input.Repository = events.NormalizeRepository(input.Repository)
	input.Command = strings.TrimSpace(input.Command)
	input.PreviewURL = strings.TrimSpace(input.PreviewURL)
	if input.Command == "" {
		input.Command = "/preview"
	}
	command, arguments := events.ParseCommand(input.Command)
	if input.GitHubAppID == "" || input.Repository == "" || input.PullRequestNumber < 1 || command != input.Command || arguments != "" || input.PreviewURL != "" && !validWorkflowPreviewURL(input.PreviewURL) {
		problem(w, http.StatusBadRequest, "Preview trigger invalid", "Provide a GitHub App, repository, pull request number, and a command such as /preview.")
		return false
	}
	connection, err := a.store.GetGitHubApp(r.Context(), input.GitHubAppID)
	if err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return false
	}
	if connection.State != "ready" || connection.InstallationID < 1 {
		problem(w, http.StatusConflict, "GitHub App unavailable", "Choose an installed GitHub App.")
		return false
	}
	documents, err := workflowservice.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		problem(w, http.StatusConflict, "Temporary workflow invalid", "Stored Application document is invalid.")
		return false
	}
	for _, source := range documents[0].Spec.Sources {
		if events.NormalizeRepository(source.Repository) == input.Repository {
			return true
		}
	}
	problem(w, http.StatusBadRequest, "Preview source missing", "The pull request repository must be an Application source.")
	return false
}

func (a *API) createWorkflowPreviewTrigger(w http.ResponseWriter, r *http.Request) {
	resource, err := a.store.GetWorkflowResource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Temporary workflow resource")
		return
	}
	if !resource.Temporary || !resource.Active {
		problem(w, http.StatusConflict, "Temporary workflow unavailable", "Choose an active temporary Application resource.")
		return
	}
	var input workflowPreviewTriggerRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.validateWorkflowPreviewTrigger(w, r, resource, &input) {
		return
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.ResourceID == resource.ID && trigger.ClosedAt == nil {
			problem(w, http.StatusConflict, "Preview trigger exists", "This Application already has a comment trigger. Edit the existing trigger.")
			return
		}
		if trigger.ClosedAt == nil && trigger.Repository == input.Repository && trigger.PullRequestNumber == input.PullRequestNumber && trigger.Command == input.Command {
			problem(w, http.StatusConflict, "Preview command already used", "This PR command already controls another preview.")
			return
		}
	}
	item := core.WorkflowPreviewTrigger{ID: ulid.Make().String(), ResourceID: resource.ID, GitHubAppID: input.GitHubAppID,
		Repository: input.Repository, PullRequestNumber: input.PullRequestNumber, Command: input.Command, PreviewURL: input.PreviewURL, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateWorkflowPreviewTrigger(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) listWorkflowPreviewTriggers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkflowPreviewTriggers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) updateWorkflowPreviewTrigger(w http.ResponseWriter, r *http.Request) {
	var input workflowPreviewTriggerRequest
	if !decode(w, r, &input) {
		return
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.ID != chi.URLParam(r, "id") {
			continue
		}
		if trigger.ClosedAt != nil {
			problem(w, http.StatusConflict, "Preview trigger closed", "The preview trigger is closed.")
			return
		}
		resource, err := a.store.GetWorkflowResource(r.Context(), trigger.ResourceID)
		if err != nil {
			a.notFoundOrInternal(w, err, "Temporary workflow resource")
			return
		}
		if !resource.Temporary || !resource.Active || !a.validateWorkflowPreviewTrigger(w, r, resource, &input) {
			if !resource.Temporary || !resource.Active {
				problem(w, http.StatusConflict, "Temporary workflow unavailable", "Choose an active temporary Application resource.")
			}
			return
		}
		for _, other := range triggers {
			if other.ID != trigger.ID && other.ClosedAt == nil && other.Repository == input.Repository && other.PullRequestNumber == input.PullRequestNumber && other.Command == input.Command {
				problem(w, http.StatusConflict, "Preview command already used", "This PR command already controls another preview.")
				return
			}
		}
		trigger.GitHubAppID, trigger.Repository, trigger.PullRequestNumber = input.GitHubAppID, input.Repository, input.PullRequestNumber
		trigger.Command, trigger.PreviewURL = input.Command, input.PreviewURL
		if err := a.store.UpdateWorkflowPreviewTrigger(r.Context(), trigger); err != nil {
			a.notFoundOrInternal(w, err, "Preview trigger")
			return
		}
		updated, err := a.store.ListWorkflowPreviewTriggers(r.Context())
		if err != nil {
			a.internal(w, err)
			return
		}
		for _, item := range updated {
			if item.ID == trigger.ID {
				writeJSON(w, http.StatusOK, item)
				return
			}
		}
		return
	}
	problem(w, http.StatusNotFound, "Preview trigger not found", "The preview trigger does not exist.")
}

func (a *API) updateWorkflowPreviewTriggerURL(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PreviewURL string `json:"previewUrl"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.PreviewURL = strings.TrimSpace(input.PreviewURL)
	if !validWorkflowPreviewURL(input.PreviewURL) {
		problem(w, http.StatusBadRequest, "Preview URL invalid", "Provide an HTTPS preview URL.")
		return
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.ID != chi.URLParam(r, "id") {
			continue
		}
		if trigger.ClosedAt != nil {
			problem(w, http.StatusConflict, "Preview trigger closed", "The preview trigger is closed.")
			return
		}
		if err := a.store.UpdateWorkflowPreviewTriggerURL(r.Context(), trigger.ID, input.PreviewURL); err != nil {
			a.internal(w, err)
			return
		}
		trigger.PreviewURL = input.PreviewURL
		writeJSON(w, http.StatusOK, trigger)
		return
	}
	problem(w, http.StatusNotFound, "Preview trigger not found", "The preview trigger does not exist.")
}

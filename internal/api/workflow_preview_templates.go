package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	workflowservice "github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type workflowPreviewTemplateRequest struct {
	GitSource      *core.WorkflowPreviewTemplateGitSource `json:"gitSource,omitempty"`
	ConfigSourceID string                                 `json:"configSourceId"`
	GitHubAppID    string                                 `json:"githubAppId"`
	Name           string                                 `json:"name"`
	Repository     string                                 `json:"repository"`
	Command        string                                 `json:"command"`
	PreviewURL     string                                 `json:"previewUrl"`
	Document       string                                 `json:"document"`
	Active         bool                                   `json:"active"`
}

func (a *API) listWorkflowPreviewTemplates(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListWorkflowPreviewTemplates(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) createWorkflowPreviewTemplate(w http.ResponseWriter, r *http.Request) {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	var input workflowPreviewTemplateRequest
	if !decode(w, r, &input) {
		return
	}
	item, ok := a.validateWorkflowPreviewTemplate(w, r, input, "")
	if !ok {
		return
	}
	now := time.Now().UTC()
	item.ID, item.CreatedAt, item.UpdatedAt = ulid.Make().String(), now, now
	if err := a.store.CreateWorkflowPreviewTemplate(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateWorkflowPreviewTemplate(w http.ResponseWriter, r *http.Request) {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	id := chi.URLParam(r, "id")
	previous, err := a.store.GetWorkflowPreviewTemplate(r.Context(), id)
	if err != nil {
		a.notFoundOrInternal(w, err, "PR preview template")
		return
	}
	var input workflowPreviewTemplateRequest
	if !decode(w, r, &input) {
		return
	}
	item, ok := a.validateWorkflowPreviewTemplate(w, r, input, id)
	if !ok {
		return
	}
	item.ID, item.CreatedAt, item.UpdatedAt = id, previous.CreatedAt, time.Now().UTC()
	if err := a.store.UpdateWorkflowPreviewTemplate(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteWorkflowPreviewTemplate(w http.ResponseWriter, r *http.Request) {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	if err := a.store.DeleteWorkflowPreviewTemplate(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.notFoundOrInternal(w, err, "PR preview template")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) validateWorkflowPreviewTemplate(w http.ResponseWriter, r *http.Request, input workflowPreviewTemplateRequest, id string) (core.WorkflowPreviewTemplate, bool) {
	item := core.WorkflowPreviewTemplate{
		ConfigSourceID: strings.TrimSpace(input.ConfigSourceID), GitHubAppID: strings.TrimSpace(input.GitHubAppID),
		Name: strings.TrimSpace(input.Name), Repository: events.NormalizeRepository(input.Repository),
		Command: strings.TrimSpace(input.Command), PreviewURL: strings.TrimSpace(input.PreviewURL),
		Document: input.Document, Active: input.Active,
	}
	if item.Command == "" {
		item.Command = "/preview"
	}
	if item.ConfigSourceID == "" || item.GitHubAppID == "" || item.Name == "" || len(item.Name) > 120 || !workflowservice.HasInstanceID(item.PreviewURL) {
		problem(w, http.StatusBadRequest, "PR preview template invalid", "Provide a name, repository access source, installed GitHub App, and preview URL containing {{ instance.id }}.")
		return core.WorkflowPreviewTemplate{}, false
	}
	source, err := a.store.GetConfigSource(r.Context(), item.ConfigSourceID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Repository access source")
		return core.WorkflowPreviewTemplate{}, false
	}
	if !source.Active {
		problem(w, http.StatusConflict, "Repository access unavailable", "Choose an active repository configuration.")
		return core.WorkflowPreviewTemplate{}, false
	}
	connection, err := a.store.GetGitHubApp(r.Context(), item.GitHubAppID)
	if err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return core.WorkflowPreviewTemplate{}, false
	}
	if connection.State != "ready" || connection.InstallationID < 1 {
		problem(w, http.StatusConflict, "GitHub App unavailable", "Choose an installed GitHub App.")
		return core.WorkflowPreviewTemplate{}, false
	}
	if input.GitSource != nil {
		item.GitSource = &core.WorkflowPreviewTemplateGitSource{Repository: events.NormalizeRepository(input.GitSource.Repository), Branch: strings.TrimSpace(input.GitSource.Branch), Path: strings.TrimSpace(input.GitSource.Path)}
		if err := validatePreviewTemplateGitSource(item.GitSource); err != nil {
			problem(w, http.StatusBadRequest, "GitHub template source invalid", err.Error())
			return core.WorkflowPreviewTemplate{}, false
		}
		var err error
		item, err = a.fetchPreviewTemplateGitSource(r.Context(), item)
		if err != nil {
			problem(w, http.StatusBadGateway, "GitHub template sync failed", err.Error())
			return core.WorkflowPreviewTemplate{}, false
		}
	}
	if err := applyPreviewTemplateTrigger(&item); err != nil {
		problem(w, http.StatusUnprocessableEntity, "Template trigger invalid", err.Error())
		return core.WorkflowPreviewTemplate{}, false
	}
	if err := validatePreviewTemplateDocument(item); err != nil {
		problem(w, http.StatusUnprocessableEntity, "Application template invalid", err.Error())
		return core.WorkflowPreviewTemplate{}, false
	}
	item.ID = id
	if err := a.validatePreviewTemplateWatchConflicts(r.Context(), item); err != nil {
		problem(w, http.StatusConflict, "Preview trigger conflict", err.Error())
		return core.WorkflowPreviewTemplate{}, false
	}

	return item, true
}

func validatePreviewTemplateDocument(item core.WorkflowPreviewTemplate) error {
	if len(item.Document) > 1<<20 || !workflowservice.HasInstanceID(item.Document) {
		return errors.New("provide WorkflowTemplate YAML under 1 MiB containing {{ instance.id }}")
	}
	rendered, err := workflowservice.RenderWorkflowTemplate([]byte(item.Document), workflowservice.TemplateVariables{ID: "p0pr0", PRNumber: 123, Repository: item.Repository, PRURL: "https://github.example/org/repo/pull/123"})
	if err != nil {
		return err
	}
	documents, err := workflowservice.Parse("preview-template.yaml", rendered)
	if err != nil || len(documents) != 1 || documents[0].Kind != workflowservice.KindApplication || documents[0].Spec == nil || !strings.Contains(documents[0].Metadata.Name, "p0pr0") {
		return errors.New("provide one valid WorkflowTemplate YAML document with {{ instance.id }} in its name")
	}
	for _, deployment := range documents[0].Spec.Deployments {
		if deployment.Helm.ReleaseName != "" && !strings.Contains(deployment.Helm.ReleaseName, "p0pr0") {
			return errors.New("put {{ instance.id }} in each Helm release name so PRs cannot share a release")
		}
	}
	for _, repository := range previewTemplateRepositories(item) {
		matched := false
		for _, candidate := range documents[0].Spec.Sources {
			if events.NormalizeRepository(candidate.Repository) == repository {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("the template must contain watched repository %s as a source", repository)
		}
	}

	return nil
}

func previewTemplateRepositories(item core.WorkflowPreviewTemplate) []string {
	if len(item.WatchRepositories) > 0 {
		return item.WatchRepositories
	}
	return []string{item.Repository}
}

func applyPreviewTemplateTrigger(item *core.WorkflowPreviewTemplate) error {
	trigger, sources, err := workflowservice.ReadWorkflowTemplateTrigger([]byte(item.Document))
	if err != nil {
		return err
	}
	item.WatchRepositories = nil
	if trigger != nil {
		item.Command = trigger.Command
		for _, alias := range trigger.Sources {
			repository := events.NormalizeRepository(sources[alias].Repository)
			if !slices.Contains(item.WatchRepositories, repository) {
				item.WatchRepositories = append(item.WatchRepositories, repository)
			}
		}
		item.Repository = item.WatchRepositories[0]
	}
	command, arguments := events.ParseCommand(item.Command)
	if command != item.Command || command == "" || arguments != "" {
		return errors.New("provide one comment command such as /preview")
	}
	for _, repository := range previewTemplateRepositories(*item) {
		owner, repo, ok := strings.Cut(repository, "/")
		if !ok || owner == "" || repo == "" || strings.ContainsAny(repo, "/{} \t\r\n") || strings.ContainsAny(owner, "{} \t\r\n") {
			return errors.New("watched sources must use literal owner/repository names")
		}
	}
	url, err := workflowservice.RenderTemplateText(item.PreviewURL, workflowservice.TemplateVariables{ID: "example", PRNumber: 123, Repository: item.Repository, PRURL: "https://github.example/org/repo/pull/123"})
	if err != nil {
		return err
	}
	if !validWorkflowPreviewURL(url) {
		return errors.New("provide a valid HTTPS preview URL pattern")
	}
	return nil
}

func (a *API) validatePreviewTemplateWatchConflicts(ctx context.Context, item core.WorkflowPreviewTemplate) error {
	if !item.Active {
		return nil
	}
	items, err := a.store.ListWorkflowPreviewTemplates(ctx)
	if err != nil {
		return err
	}
	for _, other := range items {
		if other.ID == item.ID || !other.Active || other.GitHubAppID != item.GitHubAppID || other.Command != item.Command {
			continue
		}
		for _, repo := range previewTemplateRepositories(item) {
			if slices.Contains(previewTemplateRepositories(other), repo) {
				return fmt.Errorf("template %s already watches %s for %s", other.Name, repo, item.Command)
			}
		}
	}
	return nil
}

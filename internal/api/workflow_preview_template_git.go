package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func validatePreviewTemplateGitSource(source *core.WorkflowPreviewTemplateGitSource) error {
	owner, repo, ok := strings.Cut(source.Repository, "/")
	ext := strings.ToLower(path.Ext(source.Path))
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") || strings.ContainsAny(source.Repository, " \t\r\n") {
		return errors.New("use owner/repository for the GitHub template repository")
	}
	if source.Branch == "" || strings.ContainsAny(source.Branch, " \t\r\n~^:?*[\\") || strings.Contains(source.Branch, "..") || strings.HasPrefix(source.Branch, "-") {
		return errors.New("provide a valid template branch or tag")
	}
	if source.Path == "" || len(source.Path) > 512 || path.IsAbs(source.Path) || path.Clean(source.Path) != source.Path || strings.HasPrefix(source.Path, "../") || (ext != ".yaml" && ext != ".yml" && ext != ".json") {
		return errors.New("provide a relative YAML or JSON file path within the template repository")
	}
	return nil
}

// Resolve the branch once, then read the file at that exact commit. Invalid
// changes never replace the last validated template definition.
func (a *API) fetchPreviewTemplateGitSource(ctx context.Context, item core.WorkflowPreviewTemplate) (core.WorkflowPreviewTemplate, error) {
	if item.GitSource == nil {
		return item, nil
	}
	source := *item.GitSource
	item.GitSource = &source
	if err := validatePreviewTemplateGitSource(&source); err != nil {
		return item, err
	}
	if a.eventConfig.GitHubApps == nil {
		return item, errors.New("GitHub App access is not configured")
	}
	sha, err := a.eventConfig.GitHubApps.RepositoryHead(ctx, item.GitHubAppID, source.Repository, source.Branch)
	if err != nil {
		return item, fmt.Errorf("read template branch: %w", err)
	}
	if sha != source.CommitSHA || source.LastError != "" || item.Document == "" {
		files, err := a.eventConfig.GitHubApps.RepositoryFiles(ctx, item.GitHubAppID, source.Repository, sha, source.Path)
		if err != nil {
			return item, fmt.Errorf("read template file: %w", err)
		}
		if len(files) != 1 || files[0].Path != source.Path {
			return item, errors.New("choose one template YAML file, not a directory")
		}
		item.Document = string(files[0].Contents)
	}
	if err := applyPreviewTemplateTrigger(&item); err != nil {
		return item, err
	}
	if err := validatePreviewTemplateDocument(item); err != nil {
		return item, err
	}
	now := time.Now().UTC()
	source.CommitSHA, source.SyncedAt, source.LastError = sha, &now, ""
	return item, nil
}

func (a *API) syncPreviewTemplate(ctx context.Context, id string) (core.WorkflowPreviewTemplate, error) {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	item, err := a.store.GetWorkflowPreviewTemplate(ctx, id)
	if err != nil {
		return item, err
	}
	if item.GitSource == nil {
		return item, errors.New("this template uses YAML saved in Dispatch")
	}
	refreshed, syncErr := a.fetchPreviewTemplateGitSource(ctx, item)
	if syncErr == nil {
		syncErr = a.validatePreviewTemplateWatchConflicts(ctx, refreshed)
	}
	if syncErr != nil {
		item.GitSource.LastError = syncErr.Error()
	} else {
		item = refreshed
	}
	if err := a.store.UpdateWorkflowPreviewTemplate(ctx, item); err != nil {
		return item, err
	}
	return item, syncErr
}

func (a *API) syncWorkflowPreviewTemplates(ctx context.Context) error {
	items, err := a.store.ListWorkflowPreviewTemplates(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, item := range items {
		if item.GitSource == nil {
			continue
		}
		if _, err := a.syncPreviewTemplate(ctx, item.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			joined = errors.Join(joined, fmt.Errorf("template %s: %w", item.Name, err))
		}
	}
	return joined
}

func (a *API) syncWorkflowPreviewTemplate(w http.ResponseWriter, r *http.Request) {
	item, err := a.syncPreviewTemplate(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		a.notFoundOrInternal(w, err, "PR preview template")
		return
	}
	if err != nil {
		problem(w, http.StatusBadGateway, "Template sync failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, item)
}

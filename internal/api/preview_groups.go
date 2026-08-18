package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/groups"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type previewGroupRequest struct {
	Name        string                       `json:"name"`
	GitHubAppID string                       `json:"githubAppId"`
	Command     string                       `json:"command"`
	Enabled     *bool                        `json:"enabled"`
	Components  []core.PreviewGroupComponent `json:"components"`
}

func (a *API) listPreviewGroups(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPreviewGroups(r.Context())
	a.list(w, items, err)
}

func (a *API) getPreviewGroup(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetPreviewGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Preview group")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) createPreviewGroup(w http.ResponseWriter, r *http.Request) {
	var input previewGroupRequest
	if !decode(w, r, &input) {
		return
	}
	now := time.Now().UTC()
	item := core.PreviewGroup{ID: ulid.Make().String(), Name: input.Name, GitHubAppID: strings.TrimSpace(input.GitHubAppID), Command: input.Command, Enabled: true,
		Components: input.Components, CreatedAt: now, UpdatedAt: now}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	preparePreviewGroupComponents(&item)
	if err := groups.Validate(r.Context(), a.store, &item, a.eventConfig.DefaultCommand); err != nil {
		previewGroupProblem(w, err)
		return
	}
	if err := a.validatePreviewGroupRepositories(r, item); err != nil {
		previewGroupProblem(w, err)
		return
	}
	if err := a.store.CreatePreviewGroup(r.Context(), item); err != nil {
		previewGroupProblem(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updatePreviewGroup(w http.ResponseWriter, r *http.Request) {
	existing, err := a.store.GetPreviewGroup(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Preview group")
		return
	}
	var input previewGroupRequest
	if !decode(w, r, &input) {
		return
	}
	existing.Name, existing.GitHubAppID, existing.Command, existing.Components, existing.UpdatedAt = input.Name, strings.TrimSpace(input.GitHubAppID), input.Command, input.Components, time.Now().UTC()
	if input.Enabled != nil {
		existing.Enabled = *input.Enabled
	}
	preparePreviewGroupComponents(&existing)
	if err := groups.Validate(r.Context(), a.store, &existing, a.eventConfig.DefaultCommand); err != nil {
		previewGroupProblem(w, err)
		return
	}
	if err := a.validatePreviewGroupRepositories(r, existing); err != nil {
		previewGroupProblem(w, err)
		return
	}
	if err := a.store.UpdatePreviewGroup(r.Context(), existing); err != nil {
		previewGroupProblem(w, err)
		return
	}
	writeJSON(w, http.StatusOK, existing)
}

func preparePreviewGroupComponents(group *core.PreviewGroup) {
	for index := range group.Components {
		group.Components[index].ID = ulid.Make().String()
		group.Components[index].GroupID = group.ID
		group.Components[index].Alias = strings.TrimSpace(group.Components[index].Alias)
	}
}

func (a *API) validatePreviewGroupRepositories(r *http.Request, group core.PreviewGroup) error {
	if group.GitHubAppID == "" {
		return nil
	}
	installed, err := a.githubRepositoryAccess(r.Context(), group.GitHubAppID)
	if err != nil {
		return err
	}
	for _, component := range group.Components {
		if !installed[component.Repository] {
			return errors.New("repository " + component.Repository + " is not installed for the selected GitHub App")
		}
	}
	return nil
}

func (a *API) deletePreviewGroup(w http.ResponseWriter, r *http.Request) {
	err := a.store.DeletePreviewGroup(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrPreviewGroupActive) {
		problem(w, http.StatusConflict, "Preview group is active", "Clean up every active run before deleting this preview group.")
		return
	}
	if err != nil {
		a.notFoundOrInternal(w, err, "Preview group")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listPreviewGroupRuns(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPreviewGroupRuns(r.Context(), strings.TrimSpace(r.URL.Query().Get("groupId")))
	a.list(w, items, err)
}

func (a *API) getPreviewGroupRun(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetPreviewGroupRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Preview group run")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) cleanupPreviewGroupRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := a.store.GetPreviewGroupRun(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Preview group run")
		return
	}
	if err := a.groups.Cleanup(r.Context(), id); err != nil {
		problem(w, http.StatusConflict, "Preview cleanup failed", err.Error())
		return
	}
	item, err := a.store.GetPreviewGroupRun(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func previewGroupProblem(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrPreviewGroupOverlap) {
		problem(w, http.StatusConflict, "Preview group overlaps an existing trigger", "Disable or remove the existing group that uses the same repository and command.")
		return
	}
	problem(w, http.StatusBadRequest, "Invalid preview group", err.Error())
}

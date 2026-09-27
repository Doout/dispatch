package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListProjects(r.Context())
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
	for _, item := range items {
		if visible[item.ID] {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
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

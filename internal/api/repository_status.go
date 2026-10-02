package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/go-chi/chi/v5"
)

func (a *API) listGitHubAppBranches(w http.ResponseWriter, r *http.Request) {
	a.repositoryBranches(w, r, chi.URLParam(r, "id"))
}
func (a *API) repositoryBranches(w http.ResponseWriter, r *http.Request, appID string) {
	if a.eventConfig.GitHubApps == nil || appID == "" {
		problem(w, 503, "Repository access unavailable", "Select a configured GitHub App.")
		return
	}
	expectedID, err := strconv.ParseInt(r.URL.Query().Get("repositoryId"), 10, 64)
	if err != nil || expectedID < 1 {
		problem(w, 400, "Repository identity required", "Choose an accessible repository before loading branches.")
		return
	}
	items, err := a.eventConfig.GitHubApps.RepositoryBranches(r.Context(), appID, strings.TrimSpace(r.URL.Query().Get("repository")), expectedID)
	if err != nil {
		problem(w, 422, "Branches unavailable", err.Error())
		return
	}
	writeJSON(w, 200, items)
}
func (a *API) listConfigSourceRepositories(w http.ResponseWriter, r *http.Request) {
	source, err := a.store.GetConfigSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	if a.eventConfig.GitHubApps == nil || source.GitHubAppID == "" {
		problem(w, 422, "Repository catalog unavailable", "This source uses a saved Git credential. Enter its exact clone URL and branch.")
		return
	}
	items, err := a.eventConfig.GitHubApps.ListRepositories(r.Context(), source.GitHubAppID)
	if err != nil {
		problem(w, 422, "Repository catalog unavailable", "Check the GitHub App installation and retry. The selected repository has not changed.")
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		visible := []githubapp.Repository{}
		for _, item := range items {
			if source.RepositoryID > 0 && item.ID == source.RepositoryID || source.RepositoryID == 0 && strings.EqualFold(item.FullName, source.Repository) {
				visible = append(visible, item)
			}
		}
		items = visible
	}
	writeJSON(w, 200, items)
}
func (a *API) listConfigSourceBranches(w http.ResponseWriter, r *http.Request) {
	source, err := a.store.GetConfigSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		expectedID, _ := strconv.ParseInt(r.URL.Query().Get("repositoryId"), 10, 64)
		if source.RepositoryID > 0 && expectedID != source.RepositoryID || source.RepositoryID == 0 && !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("repository")), source.Repository) {
			problem(w, 403, "Repository access denied", "Load branches for the source's configured repository.")
			return
		}
	}
	a.repositoryBranches(w, r, source.GitHubAppID)
}
func (a *API) checkConfigSourceRepository(w http.ResponseWriter, r *http.Request) {
	source, err := a.store.GetConfigSource(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	if source.GitHubAppID == "" {
		problem(w, 422, "Repository check unavailable", "This source uses a saved Git credential. Sync to check its clone URL and branch.")
		return
	}
	source, err = a.workflows.CheckSourceRepository(r.Context(), source.ID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Configuration source")
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		source = redactConfigSourceCredentials(source)
	}
	writeJSON(w, 200, source)
}

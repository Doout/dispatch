package api

import (
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/oklog/ulid/v2"
)

func (a *API) testGitHubAppConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		ID               string                 `json:"id"`
		WebURL           *string                `json:"webUrl"`
		APIURL           *string                `json:"apiUrl"`
		PrivateNetworkID *string                `json:"privateNetworkId"`
		Proxy            *githubAppProxyRequest `json:"proxy"`
	}
	if !decode(w, r, &input) {
		return
	}
	app := core.GitHubAppConnection{ID: ulid.Make().String()}
	var err error
	if input.ID != "" {
		app, err = a.store.GetGitHubApp(r.Context(), input.ID)
		if err != nil {
			a.notFoundOrInternal(w, err, "GitHub App")
			return
		}
	}
	if input.WebURL != nil {
		app.WebURL = *input.WebURL
	}
	if input.APIURL != nil {
		app.APIURL = *input.APIURL
	}
	app.WebURL, app.APIURL, err = githubapp.NormalizeEndpoints(app.WebURL, app.APIURL)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid GitHub address", err.Error())
		return
	}
	if input.PrivateNetworkID != nil {
		app.PrivateNetworkID = strings.TrimSpace(*input.PrivateNetworkID)
	}
	if err = a.validateEdgeRoute(r.Context(), app.PrivateNetworkID); err != nil {
		problem(w, http.StatusBadRequest, "Invalid network route", err.Error())
		return
	}
	app.Proxy, err = a.prepareGitHubAppProxy(app.ID, input.Proxy, app.Proxy)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid GitHub App proxy", err.Error())
		return
	}
	if a.checkGitHubAppConnection(w, r, app) {
		writeJSON(w, http.StatusOK, map[string]bool{"reachable": true})
	}
}

func (a *API) checkGitHubAppConnection(w http.ResponseWriter, r *http.Request, app core.GitHubAppConnection) bool {
	if a.eventConfig.GitHubApps == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub Apps are unavailable", "Configure encrypted storage before adding a GitHub App.")
		return false
	}
	if err := a.eventConfig.GitHubApps.TestConnection(r.Context(), app); err != nil {
		problem(w, http.StatusBadGateway, "GitHub connection test failed", err.Error())
		return false
	}
	return true
}

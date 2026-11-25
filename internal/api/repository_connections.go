package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type repositoryConnectionRequest struct {
	ProjectID          string `json:"projectId"`
	Name               string `json:"name"`
	RepositoryURL      string `json:"repositoryUrl"`
	GitHubAppID        string `json:"githubAppId"`
	CredentialSecretID string `json:"credentialSecretId"`
}

func (a *API) listRepositoryConnections(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListRepositoryConnections(r.Context())
	a.list(w, items, err)
}

func (a *API) saveRepositoryConnection(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var input repositoryConnectionRequest
	if !decode(w, r, &input) {
		return
	}
	id := chi.URLParam(r, "id")
	item := core.RepositoryConnection{
		ProjectID: strings.TrimSpace(input.ProjectID), Name: strings.TrimSpace(input.Name), RepositoryURL: strings.TrimSpace(input.RepositoryURL),
		GitHubAppID: strings.TrimSpace(input.GitHubAppID), CredentialSecretID: strings.TrimSpace(input.CredentialSecretID),
	}
	if item.RepositoryURL != "" {
		canonical, err := core.CanonicalRepositoryURL(item.RepositoryURL)
		if err != nil {
			problem(w, http.StatusBadRequest, "Invalid repository URL", err.Error())
			return
		}
		item.RepositoryURL = canonical
	}
	if id != "" {
		saved, err := a.store.GetRepositoryConnection(ctx, id)
		if err != nil {
			a.notFoundOrInternal(w, err, "Repository connection")
			return
		}
		if item.ProjectID != "" && item.ProjectID != saved.ProjectID || item.Name != "" && item.Name != saved.Name || item.RepositoryURL != "" && item.RepositoryURL != saved.RepositoryURL {
			problem(w, http.StatusBadRequest, "Connection identity cannot change", "Create a new connection to use another project, name, or repository.")
			return
		}
		item.ID, item.ProjectID, item.Name, item.RepositoryURL = saved.ID, saved.ProjectID, saved.Name, saved.RepositoryURL
	} else {
		if item.ProjectID == "" || item.Name == "" || item.RepositoryURL == "" || len(item.Name) > 80 || strings.ContainsFunc(item.Name, unicode.IsControl) {
			problem(w, http.StatusBadRequest, "Invalid repository connection", "Choose a project, enter a name of up to 80 characters, and enter the HTTPS repository URL.")
			return
		}
		if _, err := a.store.GetProject(ctx, item.ProjectID); err != nil {
			a.notFoundOrInternal(w, err, "Project")
			return
		}
		item.ID = ulid.Make().String()
	}
	if (item.GitHubAppID == "") == (item.CredentialSecretID == "") {
		problem(w, http.StatusBadRequest, "Choose one credential", "Select a GitHub token or an installed GitHub App.")
		return
	}
	if item.GitHubAppID != "" {
		app, err := a.store.GetGitHubApp(ctx, item.GitHubAppID)
		if err != nil {
			a.notFoundOrInternal(w, err, "GitHub App")
			return
		}
		if app.State != "ready" || app.InstallationID <= 0 || githubapp.ValidateRepositoryHost(item.RepositoryURL, app.WebURL) != nil {
			problem(w, http.StatusBadRequest, "Incompatible GitHub App", "Select a verified GitHub App installed on the repository host.")
			return
		}
	} else {
		secret, err := a.store.GetSecret(ctx, item.CredentialSecretID)
		if err != nil {
			a.notFoundOrInternal(w, err, "GitHub token")
			return
		}
		if secret.Type != core.SecretTypeGitHubToken {
			problem(w, http.StatusBadRequest, "Incompatible credential", "Select a secret with type GitHub token.")
			return
		}
	}
	item.UpdatedAt = time.Now().UTC()
	if id == "" {
		err := a.store.CreateRepositoryConnection(ctx, item)
		if errors.Is(err, store.ErrAlreadyExists) {
			connectionNameConflict(w)
			return
		}
		if err != nil {
			a.internal(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, item)
		return
	}
	if err := a.store.UpdateRepositoryConnection(ctx, item); err != nil {
		a.notFoundOrInternal(w, err, "Repository connection")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func connectionNameConflict(w http.ResponseWriter) {
	problem(w, http.StatusConflict, "Connection name in use", "Use a different name for this connection.")
}

func (a *API) repositoryCredentialUnused(w http.ResponseWriter, r *http.Request, appID, secretID string) bool {
	connections, err := a.store.ListRepositoryConnections(r.Context())
	if err != nil {
		a.internal(w, err)
		return false
	}
	for _, connection := range connections {
		if appID != "" && connection.GitHubAppID == appID || secretID != "" && connection.CredentialSecretID == secretID {
			problem(w, http.StatusConflict, "Credential in use", "Switch the credential on repository connection "+connection.Name+" first.")
			return false
		}
	}
	return true
}

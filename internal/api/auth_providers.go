package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type authProviderRequest struct {
	Name         string  `json:"name"`
	Type         string  `json:"type"`
	BaseURL      string  `json:"baseUrl"`
	APIURL       string  `json:"apiUrl"`
	ClientID     string  `json:"clientId"`
	ClientSecret *string `json:"clientSecret"`
	Provisioning string  `json:"provisioning"`
	State        string  `json:"state"`
}

type authProviderManifestRequest struct {
	authProviderRequest
	OwnerType string `json:"ownerType"`
	Owner     string `json:"owner"`
}

type authProviderManifestState struct {
	Provider  core.AuthProvider
	ExpiresAt time.Time
}

type publicAuthProvider struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	BaseURL string `json:"baseUrl"`
}

func publicProvider(item core.AuthProvider) publicAuthProvider {
	return publicAuthProvider{ID: item.ID, Name: item.Name, Type: item.Type, BaseURL: item.BaseURL}
}

func (a *API) publicAuthProviders(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []publicAuthProvider{}
	for _, item := range items {
		if item.State == core.AuthProviderStateReady {
			result = append(result, publicProvider(item))
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func normalizeAuthProvider(input authProviderRequest, current *core.AuthProvider, requireClientID bool) (core.AuthProvider, string, error) {
	item := core.AuthProvider{}
	if current != nil {
		item = *current
	}
	item.Name = strings.TrimSpace(input.Name)
	item.Type = strings.TrimSpace(input.Type)
	if item.Type == "" {
		item.Type = core.AuthProviderGitHub
	}
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(input.BaseURL), "/"))
	if err != nil || base.Scheme != "https" || base.Host == "" || base.RawQuery != "" || base.Fragment != "" {
		return item, "", errors.New("GitHub URL must be an HTTPS origin")
	}
	base.Path = strings.TrimRight(base.Path, "/")
	item.BaseURL = base.String()
	item.APIURL = strings.TrimRight(strings.TrimSpace(input.APIURL), "/")
	if item.APIURL == "" {
		if strings.EqualFold(base.Host, "github.com") {
			item.APIURL = "https://api.github.com"
		} else {
			item.APIURL = item.BaseURL + "/api/v3"
		}
	}
	apiURL, err := url.Parse(item.APIURL)
	if err != nil || apiURL.Scheme != "https" || apiURL.Host == "" {
		return item, "", errors.New("GitHub API URL must be HTTPS")
	}
	item.ClientID = strings.TrimSpace(input.ClientID)
	item.Provisioning = input.Provisioning
	if item.Provisioning == "" {
		item.Provisioning = core.AuthProvisionExisting
	}
	item.State = input.State
	if item.State == "" {
		item.State = core.AuthProviderStateReady
	}
	secret := ""
	if input.ClientSecret != nil {
		secret = strings.TrimSpace(*input.ClientSecret)
	}
	if item.Name == "" || item.Type != core.AuthProviderGitHub || requireClientID && item.ClientID == "" {
		return item, secret, errors.New("Name, client ID, and a supported provider are required")
	}
	if item.Provisioning != core.AuthProvisionExisting && item.Provisioning != core.AuthProvisionApproval {
		return item, secret, errors.New("Unknown account policy")
	}
	if item.State != core.AuthProviderStateReady && item.State != core.AuthProviderStateDisabled {
		return item, secret, errors.New("Unknown provider state")
	}
	return item, secret, nil
}

func authProviderNameTaken(items []core.AuthProvider, name, exceptID string) bool {
	for _, item := range items {
		if item.ID != exceptID && strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return true
		}
	}
	return false
}

func (a *API) startAuthProviderManifest(w http.ResponseWriter, r *http.Request) {
	var input authProviderManifestRequest
	if !decode(w, r, &input) {
		return
	}
	if a.eventConfig.GitHubApps == nil || a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub setup unavailable", "Configure encrypted credential storage before creating a sign-in method.")
		return
	}
	item, _, err := normalizeAuthProvider(input.authProviderRequest, nil, false)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid sign-in method", err.Error())
		return
	}
	input.OwnerType = strings.ToLower(strings.TrimSpace(input.OwnerType))
	input.Owner = strings.TrimSpace(input.Owner)
	if input.OwnerType != "personal" && input.OwnerType != "organization" {
		problem(w, http.StatusBadRequest, "Registration owner required", "Choose your personal account or a GitHub organization.")
		return
	}
	if input.OwnerType == "organization" && !githubOwnerPattern.MatchString(input.Owner) {
		problem(w, http.StatusBadRequest, "Invalid GitHub owner", "Enter an organization login without spaces.")
		return
	}
	if input.OwnerType == "personal" {
		input.Owner = ""
	}
	providers, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if authProviderNameTaken(providers, item.Name, "") {
		problem(w, http.StatusConflict, "Name already used", "Choose a different sign-in method name.")
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		a.internal(w, err)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(random)
	appName := "Dispatch-login-" + hex.EncodeToString(random[:4])
	item.ID = ulid.Make().String()
	item.ClientID = ""
	origin := externalOrigin(r)
	callbackURL := origin + "/api/v1/auth/providers/manifest/callback"
	action := item.BaseURL + "/settings/apps/new?state=" + url.QueryEscape(state)
	if input.OwnerType == "organization" {
		action = item.BaseURL + "/organizations/" + url.PathEscape(input.Owner) + "/settings/apps/new?state=" + url.QueryEscape(state)
	}
	manifest := map[string]interface{}{
		"name":                     appName,
		"url":                      origin,
		"description":              "Signs users in to Dispatch.",
		"redirect_url":             callbackURL,
		"callback_urls":            []string{origin + "/api/v1/auth/callback"},
		"public":                   true,
		"request_oauth_on_install": false,
		"default_permissions":      map[string]string{"emails": "read"},
	}
	now := time.Now().UTC()
	a.manifestMu.Lock()
	for pendingState, pending := range a.authManifestStates {
		if now.After(pending.ExpiresAt) {
			delete(a.authManifestStates, pendingState)
		}
	}
	a.authManifestStates[state] = authProviderManifestState{Provider: item, ExpiresAt: now.Add(time.Hour)}
	a.manifestMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"action": action, "manifest": manifest})
}

func (a *API) completeAuthProviderManifest(w http.ResponseWriter, r *http.Request) {
	state, code := strings.TrimSpace(r.URL.Query().Get("state")), strings.TrimSpace(r.URL.Query().Get("code"))
	a.manifestMu.Lock()
	pending, ok := a.authManifestStates[state]
	if ok {
		delete(a.authManifestStates, state)
	}
	a.manifestMu.Unlock()
	redirect := func(status, detail string) {
		http.Redirect(w, r, "/?view=access&accessSection=signin&authProviderStatus="+url.QueryEscape(status)+"&detail="+url.QueryEscape(detail), http.StatusSeeOther)
	}
	if !ok || state == "" || code == "" || time.Now().UTC().After(pending.ExpiresAt) {
		redirect("error", "The GitHub setup expired. Start again from Access.")
		return
	}
	conversion, err := a.eventConfig.GitHubApps.ConvertManifest(r.Context(), pending.Provider.APIURL, code)
	if err != nil {
		redirect("error", err.Error())
		return
	}
	if strings.TrimSpace(conversion.ClientID) == "" || strings.TrimSpace(conversion.ClientSecret) == "" {
		redirect("error", "GitHub did not return OAuth credentials for the App.")
		return
	}
	providers, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		redirect("error", "Could not check existing sign-in methods.")
		return
	}
	if authProviderNameTaken(providers, pending.Provider.Name, "") {
		redirect("error", "A sign-in method with this name already exists.")
		return
	}
	item := pending.Provider
	item.ClientID = strings.TrimSpace(conversion.ClientID)
	item.EncryptedClientSecret, err = a.eventConfig.Vault.Encrypt("auth-provider:"+item.ID, []byte(strings.TrimSpace(conversion.ClientSecret)))
	if err != nil {
		redirect("error", "Could not encrypt the GitHub client secret.")
		return
	}
	item.ClientSecretConfigured = true
	item.CreatedAt, item.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := a.store.CreateAuthProvider(r.Context(), item); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			redirect("error", "A sign-in method with this name already exists.")
			return
		}
		redirect("error", "Could not save the sign-in method.")
		return
	}
	redirect("created", "Sign-in method created.")
}

func (a *API) createAuthProvider(w http.ResponseWriter, r *http.Request) {
	var input authProviderRequest
	if !decode(w, r, &input) {
		return
	}
	item, secret, err := normalizeAuthProvider(input, nil, true)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid sign-in method", err.Error())
		return
	}
	if a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage unavailable", "Configure the controller master key before adding a sign-in method.")
		return
	}
	if secret == "" {
		problem(w, http.StatusBadRequest, "Client secret required", "Enter the OAuth client secret.")
		return
	}
	providers, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if authProviderNameTaken(providers, item.Name, "") {
		problem(w, http.StatusConflict, "Name already used", "Choose a different sign-in method name.")
		return
	}
	item.ID = ulid.Make().String()
	item.EncryptedClientSecret, err = a.eventConfig.Vault.Encrypt("auth-provider:"+item.ID, []byte(secret))
	if err != nil {
		a.internal(w, err)
		return
	}
	item.ClientSecretConfigured = true
	item.CreatedAt, item.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := a.store.CreateAuthProvider(r.Context(), item); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			problem(w, http.StatusConflict, "Name already used", "Choose a different sign-in method name.")
			return
		}
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateAuthProvider(w http.ResponseWriter, r *http.Request) {
	current, err := a.store.GetAuthProvider(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		problem(w, http.StatusNotFound, "Sign-in method not found", "The requested sign-in method does not exist.")
		return
	}
	var input authProviderRequest
	if !decode(w, r, &input) {
		return
	}
	item, secret, err := normalizeAuthProvider(input, &current, true)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid sign-in method", err.Error())
		return
	}
	providers, err := a.store.ListAuthProviders(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	if authProviderNameTaken(providers, item.Name, item.ID) {
		problem(w, http.StatusConflict, "Name already used", "Choose a different sign-in method name.")
		return
	}
	if secret != "" {
		if a.eventConfig.Vault == nil {
			problem(w, http.StatusServiceUnavailable, "Secret storage unavailable", "Configure the controller master key before changing the client secret.")
			return
		}
		item.EncryptedClientSecret, err = a.eventConfig.Vault.Encrypt("auth-provider:"+item.ID, []byte(secret))
		if err != nil {
			a.internal(w, err)
			return
		}
	}
	item.ClientSecretConfigured = item.EncryptedClientSecret != ""
	item.UpdatedAt = time.Now().UTC()
	if err := a.store.UpdateAuthProvider(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteAuthProvider(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteAuthProvider(r.Context(), chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Sign-in method not found", "The requested sign-in method does not exist.")
			return
		}
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) verifyAuthProvider(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetAuthProvider(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		problem(w, http.StatusNotFound, "Sign-in method not found", "The requested sign-in method does not exist.")
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, item.APIURL, nil)
	request.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 8 * time.Second}
	response, err := client.Do(request)
	if err != nil || response.StatusCode >= 500 {
		problem(w, http.StatusBadGateway, "GitHub unavailable", "Dispatch could not reach this GitHub API.")
		return
	}
	_ = response.Body.Close()
	now := time.Now().UTC()
	item.LastVerifiedAt = &now
	item.UpdatedAt = now
	if err := a.store.UpdateAuthProvider(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/groups"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type githubAppRequest struct {
	Name             string `json:"name"`
	WebURL           string `json:"webUrl"`
	APIURL           string `json:"apiUrl"`
	AppID            int64  `json:"appId"`
	ClientID         string `json:"clientId"`
	Slug             string `json:"slug"`
	InstallationID   int64  `json:"installationId"`
	PrivateKey       string `json:"privateKey"`
	WebhookSecret    string `json:"webhookSecret"`
	EventDelivery    string `json:"eventDelivery"`
	RelayServerID    string `json:"relayServerId"`
	PrivateNetworkID string `json:"privateNetworkId"`
}

type githubAppUpdateRequest struct {
	Name             *string `json:"name"`
	WebURL           *string `json:"webUrl"`
	APIURL           *string `json:"apiUrl"`
	AppID            *int64  `json:"appId"`
	ClientID         *string `json:"clientId"`
	Slug             *string `json:"slug"`
	InstallationID   *int64  `json:"installationId"`
	PrivateKey       *string `json:"privateKey"`
	WebhookSecret    *string `json:"webhookSecret"`
	PrivateNetworkID *string `json:"privateNetworkId"`
}

type githubAppManifestState struct {
	ConnectionID          string
	Name                  string
	WebURL                string
	APIURL                string
	RegistrationOwner     string
	RegistrationOwnerType string
	WebhookURL            string
	RelayWebhookID        string
	PrivateNetworkID      string
	ExpiresAt             time.Time
}

var githubOwnerPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)

func (a *API) listGitHubApps(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListGitHubApps(r.Context())
	a.list(w, items, err)
}

func (a *API) createGitHubApp(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.GitHubApps == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub Apps are unavailable", "Configure encrypted storage before adding a GitHub App.")
		return
	}
	var input githubAppRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 80 {
		problem(w, http.StatusBadRequest, "Connection name required", "Enter a name no longer than 80 characters.")
		return
	}
	if input.AppID < 1 {
		problem(w, http.StatusBadRequest, "App ID required", "Enter the numeric App ID from GitHub.")
		return
	}
	webURL, apiURL, err := githubapp.NormalizeEndpoints(input.WebURL, input.APIURL)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid GitHub address", err.Error())
		return
	}
	input.PrivateNetworkID = strings.TrimSpace(input.PrivateNetworkID)
	if err := a.validateEdgeRoute(r.Context(), input.PrivateNetworkID); err != nil {
		problem(w, http.StatusBadRequest, "Invalid network route", err.Error())
		return
	}
	id := ulid.Make().String()
	delivery, err := githubAppEventDelivery(input.EventDelivery, input.RelayServerID)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid event delivery", err.Error())
		return
	}
	if delivery == "none" && strings.TrimSpace(input.WebhookSecret) == "" {
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			a.internal(w, err)
			return
		}
		input.WebhookSecret = base64.RawURLEncoding.EncodeToString(secret)
	}
	privateKey, webhookSecret, err := a.eventConfig.GitHubApps.EncryptCredentials(id, input.PrivateKey, input.WebhookSecret)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid GitHub App credentials", err.Error())
		return
	}
	webhookURL := ""
	relayWebhookID := ""
	if delivery == "direct" {
		webhookURL = externalOrigin(r) + "/api/v1/events/github/apps/" + id
	}
	if delivery == "relay" {
		relayServer, relayErr := a.store.GetServer(r.Context(), strings.TrimSpace(input.RelayServerID))
		if relayErr != nil {
			a.notFoundOrInternal(w, relayErr, "Relay server")
			return
		}
		endpoint, relayErr := a.provisionRelayWebhook(r.Context(), relayServer, input.Name+" GitHub events", core.EventProviderGitHub, id)
		if relayErr != nil {
			problem(w, http.StatusBadGateway, "Relay endpoint could not be created", relayErr.Error())
			return
		}
		webhookURL, relayWebhookID = endpoint.URL, endpoint.ID
	}
	now := time.Now().UTC()
	item := core.GitHubAppConnection{
		ID: id, Name: input.Name, WebURL: webURL, APIURL: apiURL, AppID: input.AppID,
		ClientID: strings.TrimSpace(input.ClientID), Slug: strings.TrimSpace(input.Slug), InstallationID: input.InstallationID,
		WebhookURL: webhookURL, RelayWebhookID: relayWebhookID, PrivateNetworkID: input.PrivateNetworkID,
		EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret, CreatedAt: now, UpdatedAt: now,
	}
	item.State = githubapp.State(item)
	if err := a.store.CreateGitHubApp(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	item.PrivateKeyConfigured, item.WebhookSecretConfigured = true, true
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateGitHubApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	item, err := a.store.GetGitHubApp(r.Context(), id)
	if err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return
	}
	var input githubAppUpdateRequest
	if !decode(w, r, &input) {
		return
	}
	if input.Name != nil {
		item.Name = strings.TrimSpace(*input.Name)
	}
	if item.Name == "" || len(item.Name) > 80 {
		problem(w, http.StatusBadRequest, "Connection name required", "Enter a name no longer than 80 characters.")
		return
	}
	webURL, apiURL := item.WebURL, item.APIURL
	if input.WebURL != nil {
		webURL = *input.WebURL
	}
	if input.APIURL != nil {
		apiURL = *input.APIURL
	}
	item.WebURL, item.APIURL, err = githubapp.NormalizeEndpoints(webURL, apiURL)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid GitHub address", err.Error())
		return
	}
	if input.AppID != nil {
		item.AppID = *input.AppID
	}
	if item.AppID < 1 {
		problem(w, http.StatusBadRequest, "App ID required", "Enter the numeric App ID from GitHub.")
		return
	}
	if input.ClientID != nil {
		item.ClientID = strings.TrimSpace(*input.ClientID)
	}
	if input.Slug != nil {
		item.Slug = strings.TrimSpace(*input.Slug)
	}
	if input.InstallationID != nil {
		if *input.InstallationID < 0 {
			problem(w, http.StatusBadRequest, "Invalid installation", "Installation ID must be a positive number.")
			return
		}
		item.InstallationID = *input.InstallationID
		item.InstallationAccount = ""
		item.InstallationURL = ""
		item.LastVerifiedAt = nil
	}
	if input.PrivateNetworkID != nil {
		item.PrivateNetworkID = strings.TrimSpace(*input.PrivateNetworkID)
		if err := a.validateEdgeRoute(r.Context(), item.PrivateNetworkID); err != nil {
			problem(w, http.StatusBadRequest, "Invalid network route", err.Error())
			return
		}
		item.LastVerifiedAt = nil
	}
	if input.PrivateKey != nil || input.WebhookSecret != nil {
		if input.PrivateKey == nil || input.WebhookSecret == nil || strings.TrimSpace(*input.PrivateKey) == "" || strings.TrimSpace(*input.WebhookSecret) == "" {
			problem(w, http.StatusBadRequest, "Complete credentials required", "Replace the private key and webhook secret together.")
			return
		}
		item.EncryptedPrivateKey, item.EncryptedWebhookSecret, err = a.eventConfig.GitHubApps.EncryptCredentials(id, *input.PrivateKey, *input.WebhookSecret)
		if err != nil {
			problem(w, http.StatusBadRequest, "Invalid GitHub App credentials", err.Error())
			return
		}
		item.LastVerifiedAt = nil
	}
	item.State, item.UpdatedAt = githubapp.State(item), time.Now().UTC()
	if err := a.store.UpdateGitHubApp(r.Context(), item); err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return
	}
	a.eventConfig.GitHubApps.Invalidate(id)
	a.githubMu.Lock()
	delete(a.githubServices, id)
	a.githubMu.Unlock()
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteGitHubApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	connection, connectionErr := a.store.GetGitHubApp(r.Context(), id)
	if connectionErr != nil {
		a.notFoundOrInternal(w, connectionErr, "GitHub App")
		return
	}
	apps, err := a.store.ListApps(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, app := range apps {
		if app.SourceAuthType == "github_app" && app.SourceCredentialID == id {
			problem(w, http.StatusConflict, "GitHub App in use", "Choose another repository credential on applications and templates before deleting this connection.")
			return
		}
	}
	triggers, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.GitHubAppID == id {
			problem(w, http.StatusConflict, "GitHub App in use", "Choose another connector on event rules before deleting this connection.")
			return
		}
	}
	groups, err := a.store.ListPreviewGroups(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, group := range groups {
		if group.GitHubAppID == id {
			problem(w, http.StatusConflict, "GitHub App in use", "Choose another connector on preview groups before deleting this connection.")
			return
		}
	}
	if connection.RelayWebhookID != "" {
		endpoint, endpointErr := a.store.GetRelayWebhook(r.Context(), connection.RelayWebhookID)
		if endpointErr == nil {
			server, serverErr := a.store.GetServer(r.Context(), endpoint.ServerID)
			if serverErr != nil {
				a.internal(w, serverErr)
				return
			}
			client, clientErr := a.relayClient(r.Context(), server)
			if clientErr != nil {
				problem(w, http.StatusBadGateway, "Relay endpoint could not be removed", clientErr.Error())
				return
			}
			if clientErr = client.DeleteHook(r.Context(), endpoint.RemoteID); clientErr != nil {
				problem(w, http.StatusBadGateway, "Relay endpoint could not be removed", clientErr.Error())
				return
			}
			if endpointErr = a.store.DeleteRelayWebhook(r.Context(), endpoint.ID); endpointErr != nil {
				a.internal(w, endpointErr)
				return
			}
		}
	}
	if err := a.store.DeleteGitHubApp(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return
	}
	a.eventConfig.GitHubApps.Invalidate(id)
	a.githubMu.Lock()
	delete(a.githubServices, id)
	a.githubMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) verifyGitHubApp(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	result, err := a.eventConfig.GitHubApps.Verify(r.Context(), id)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "GitHub App verification failed", err.Error())
		return
	}
	item, err := a.store.GetGitHubApp(r.Context(), id)
	if err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return
	}
	item.Slug, item.ClientID = result.Slug, result.ClientID
	item.RegistrationOwner, item.RegistrationOwnerType = result.RegistrationOwner, result.RegistrationOwnerType
	item.InstallationAccount = result.InstallationAccount
	item.InstallationURL = result.InstallationURL
	now := time.Now().UTC()
	item.LastVerifiedAt, item.UpdatedAt = &now, now
	item.State = githubapp.State(item)
	if err := a.store.UpdateGitHubApp(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"connection": item, "verification": result})
}

func (a *API) listGitHubAppInstallations(w http.ResponseWriter, r *http.Request) {
	items, err := a.eventConfig.GitHubApps.ListInstallations(r.Context(), chi.URLParam(r, "id"))
	a.list(w, items, err)
}

func (a *API) listGitHubAppRepositories(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.GitHubApps == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub Apps are unavailable", "Configure encrypted storage before loading repositories.")
		return
	}
	items, err := a.eventConfig.GitHubApps.ListRepositories(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			a.notFoundOrInternal(w, err, "GitHub App")
			return
		}
		problem(w, http.StatusUnprocessableEntity, "Repository access failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) validateGitHubRepositoryAccess(ctx context.Context, id, repository string) error {
	installed, err := a.githubRepositoryAccess(ctx, id)
	if err != nil {
		return err
	}
	repository = strings.ToLower(strings.TrimSpace(repository))
	if installed[repository] {
		return nil
	}
	return errors.New("repository " + repository + " is not installed for the selected GitHub App")
}

func (a *API) githubRepositoryAccess(ctx context.Context, id string) (map[string]bool, error) {
	if a.eventConfig.GitHubApps == nil {
		return nil, errors.New("GitHub App repository access is not configured")
	}
	items, err := a.eventConfig.GitHubApps.ListRepositories(ctx, id)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]bool, len(items))
	for _, item := range items {
		installed[strings.ToLower(strings.TrimSpace(item.FullName))] = true
	}
	return installed, nil
}

type startManifestRequest struct {
	Name             string `json:"name"`
	WebURL           string `json:"webUrl"`
	APIURL           string `json:"apiUrl"`
	OwnerType        string `json:"ownerType"`
	Owner            string `json:"owner"`
	EventDelivery    string `json:"eventDelivery"`
	RelayServerID    string `json:"relayServerId"`
	PrivateNetworkID string `json:"privateNetworkId"`
}

func (a *API) startGitHubAppManifest(w http.ResponseWriter, r *http.Request) {
	var input startManifestRequest
	if !decode(w, r, &input) {
		return
	}
	input.Name, input.OwnerType, input.Owner = strings.TrimSpace(input.Name), strings.ToLower(strings.TrimSpace(input.OwnerType)), strings.TrimSpace(input.Owner)
	if input.Name == "" || len(input.Name) > 34 {
		problem(w, http.StatusBadRequest, "GitHub App name required", "Enter a unique name no longer than 34 characters.")
		return
	}
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
	webURL, apiURL, err := githubapp.NormalizeEndpoints(input.WebURL, input.APIURL)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid GitHub address", err.Error())
		return
	}
	input.PrivateNetworkID = strings.TrimSpace(input.PrivateNetworkID)
	if err := a.validateEdgeRoute(r.Context(), input.PrivateNetworkID); err != nil {
		problem(w, http.StatusBadRequest, "Invalid network route", err.Error())
		return
	}
	delivery, err := githubAppEventDelivery(input.EventDelivery, input.RelayServerID)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid event delivery", err.Error())
		return
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		a.internal(w, err)
		return
	}
	state := base64.RawURLEncoding.EncodeToString(random)
	connectionID := ulid.Make().String()
	origin := externalOrigin(r)
	webhookURL := ""
	relayWebhookID := ""
	if delivery == "direct" {
		webhookURL = origin + "/api/v1/events/github/apps/" + connectionID
	}
	if delivery == "relay" {
		relayServer, relayErr := a.store.GetServer(r.Context(), strings.TrimSpace(input.RelayServerID))
		if relayErr != nil {
			a.notFoundOrInternal(w, relayErr, "Relay server")
			return
		}
		endpoint, relayErr := a.provisionRelayWebhook(r.Context(), relayServer, input.Name+" GitHub events", core.EventProviderGitHub, connectionID)
		if relayErr != nil {
			problem(w, http.StatusBadGateway, "Relay endpoint could not be created", relayErr.Error())
			return
		}
		webhookURL, relayWebhookID = endpoint.URL, endpoint.ID
	}
	callbackURL := origin + "/api/v1/github-apps/manifest/callback"
	setupURL := origin + "/?view=connections&githubAppSetup=" + url.QueryEscape(connectionID)
	action := webURL + "/settings/apps/new?state=" + url.QueryEscape(state)
	if input.OwnerType == "organization" {
		action = webURL + "/organizations/" + url.PathEscape(input.Owner) + "/settings/apps/new?state=" + url.QueryEscape(state)
	}
	manifest := map[string]interface{}{
		"name":                     input.Name,
		"url":                      origin,
		"description":              "Provides repository access for Dispatch.",
		"redirect_url":             callbackURL,
		"setup_url":                setupURL,
		"setup_on_update":          true,
		"public":                   true,
		"request_oauth_on_install": false,
		"default_permissions":      map[string]string{"contents": "read", "issues": "write", "pull_requests": "read", "metadata": "read", "statuses": "write"},
	}
	if delivery != "none" {
		manifest["hook_attributes"] = map[string]interface{}{"url": webhookURL, "active": true}
		manifest["default_events"] = []string{"issue_comment", "pull_request", "push"}
	}
	a.manifestMu.Lock()
	for pendingState, pending := range a.manifestStates {
		if time.Now().UTC().After(pending.ExpiresAt) {
			delete(a.manifestStates, pendingState)
		}
	}
	a.manifestStates[state] = githubAppManifestState{ConnectionID: connectionID, Name: input.Name, WebURL: webURL,
		APIURL: apiURL, RegistrationOwner: input.Owner, RegistrationOwnerType: input.OwnerType,
		WebhookURL: webhookURL, RelayWebhookID: relayWebhookID, PrivateNetworkID: input.PrivateNetworkID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	a.manifestMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{"action": action, "manifest": manifest})
}

func githubAppEventDelivery(value, relayServerID string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		if strings.TrimSpace(relayServerID) != "" {
			return "relay", nil
		}
		return "direct", nil
	}
	if value != "none" && value != "direct" && value != "relay" {
		return "", errors.New("choose no webhook, direct webhook, or webhook relay")
	}
	if value == "relay" && strings.TrimSpace(relayServerID) == "" {
		return "", errors.New("choose a relay server")
	}
	return value, nil
}

func (a *API) completeGitHubAppManifest(w http.ResponseWriter, r *http.Request) {
	state, code := strings.TrimSpace(r.URL.Query().Get("state")), strings.TrimSpace(r.URL.Query().Get("code"))
	a.manifestMu.Lock()
	pending, ok := a.manifestStates[state]
	if ok {
		delete(a.manifestStates, state)
	}
	a.manifestMu.Unlock()
	redirect := func(status, detail string) {
		http.Redirect(w, r, "/?view=connections&githubAppStatus="+url.QueryEscape(status)+"&detail="+url.QueryEscape(detail), http.StatusSeeOther)
	}
	if !ok || state == "" || code == "" || time.Now().UTC().After(pending.ExpiresAt) {
		redirect("error", "The GitHub App setup expired. Start again from Connections.")
		return
	}
	conversion, err := a.eventConfig.GitHubApps.ConvertManifest(r.Context(), pending.APIURL, code, pending.PrivateNetworkID)
	if err != nil {
		redirect("error", err.Error())
		return
	}
	if strings.TrimSpace(conversion.WebhookSecret) == "" {
		if pending.WebhookURL != "" {
			redirect("error", "GitHub did not return a webhook secret.")
			return
		}
		secret := make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			redirect("error", "Could not prepare the polling connection.")
			return
		}
		conversion.WebhookSecret = base64.RawURLEncoding.EncodeToString(secret)
	}
	privateKey, webhookSecret, err := a.eventConfig.GitHubApps.EncryptCredentials(pending.ConnectionID, conversion.PrivateKey, conversion.WebhookSecret)
	if err != nil {
		redirect("error", err.Error())
		return
	}
	now := time.Now().UTC()
	name := pending.Name
	if strings.TrimSpace(conversion.Name) != "" {
		name = strings.TrimSpace(conversion.Name)
	}
	item := core.GitHubAppConnection{
		ID: pending.ConnectionID, Name: name, WebURL: pending.WebURL, APIURL: pending.APIURL, AppID: conversion.AppID,
		ClientID: conversion.ClientID, Slug: conversion.Slug, RegistrationOwner: conversion.RegistrationOwner,
		RegistrationOwnerType: conversion.RegistrationOwnerType, WebhookURL: pending.WebhookURL, RelayWebhookID: pending.RelayWebhookID, PrivateNetworkID: pending.PrivateNetworkID,
		EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret, State: "needs_installation",
		CreatedAt: now, UpdatedAt: now,
	}
	if item.RegistrationOwner == "" {
		item.RegistrationOwner = pending.RegistrationOwner
	}
	if item.RegistrationOwnerType == "" {
		item.RegistrationOwnerType = pending.RegistrationOwnerType
	}
	if err := a.store.CreateGitHubApp(r.Context(), item); err != nil {
		redirect("error", err.Error())
		return
	}
	http.Redirect(w, r, "/?view=connections&githubAppCreated="+url.QueryEscape(item.ID), http.StatusSeeOther)
}

func (a *API) validateEdgeRoute(ctx context.Context, id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	network, err := a.store.GetPrivateNetwork(ctx, strings.TrimSpace(id))
	if err != nil {
		return errors.New("choose an existing edge node")
	}
	if network.Driver != "dispatch_agent" {
		return errors.New("GitHub connections require a managed edge node")
	}
	return nil
}

func externalOrigin(r *http.Request) string {
	scheme := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
	if scheme != "http" && scheme != "https" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0])
	if host == "" {
		host = r.Host
	}
	return scheme + "://" + host
}

func (a *API) githubAppServices(ctx context.Context, id string) (githubEventServices, error) {
	a.githubMu.Lock()
	if services, ok := a.githubServices[id]; ok {
		a.githubMu.Unlock()
		return services, nil
	}
	a.githubMu.Unlock()
	connection, err := a.store.GetGitHubApp(ctx, id)
	if err != nil {
		return githubEventServices{}, err
	}
	tokenSource := func(ctx context.Context) (string, error) {
		return a.eventConfig.GitHubApps.InstallationToken(ctx, id)
	}
	resolver := events.GitHubResolver{BaseURL: connection.APIURL, TokenSource: tokenSource}
	notifier := events.GitHubNotifier{BaseURL: connection.APIURL, TokenSource: tokenSource}
	services := githubEventServices{
		events: events.New(a.store, resolver, a.lifecycle, notifier),
		groups: groups.New(a.store, a.deploy, resolver, notifier, nil),
	}
	a.githubMu.Lock()
	if existing, ok := a.githubServices[id]; ok {
		services = existing
	} else {
		a.githubServices[id] = services
	}
	a.githubMu.Unlock()
	return services, nil
}

func (a *API) githubAppWebhook(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.GitHubApps == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App receiver is not configured", "Configure encrypted storage before sending events.")
		return
	}
	id := chi.URLParam(r, "id")
	connection, err := a.store.GetGitHubApp(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "GitHub App not found", "The webhook URL does not match a configured GitHub App.")
			return
		}
		a.internal(w, err)
		return
	}
	secret, err := a.eventConfig.GitHubApps.WebhookSecret(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "GitHub App not found", "The webhook URL does not match a configured GitHub App.")
			return
		}
		a.internal(w, err)
		return
	}
	services, err := a.githubAppServices(r.Context(), id)
	if err != nil {
		a.internal(w, err)
		return
	}
	a.processGitHubWebhook(w, r, secret, id, connection.InstallationID, services.groups, services.events)
}

func (a *API) processGitHubWebhook(w http.ResponseWriter, r *http.Request, secret, connectionID string, installationID int64, groupService *groups.Service, eventService *events.Service) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid webhook body", "Keep the webhook payload under 1 MB.")
		return
	}
	if err := events.VerifySignature(secret, body, r.Header.Get("X-Hub-Signature-256")); err != nil {
		problem(w, http.StatusUnauthorized, "Invalid webhook signature", "Sign the request body with the configured webhook secret.")
		return
	}
	if installationID > 0 {
		var envelope struct {
			Installation *struct {
				ID int64 `json:"id"`
			} `json:"installation"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil || envelope.Installation == nil || envelope.Installation.ID != installationID {
			problem(w, http.StatusForbidden, "Unexpected GitHub App installation", "The event does not belong to this connection's installation.")
			return
		}
	}
	if r.Header.Get("X-GitHub-Event") == "push" {
		if connectionID == "" || a.workflows == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		created, err := a.workflows.HandlePush(r.Context(), connectionID, r.Header.Get("X-GitHub-Delivery"), body)
		if err != nil {
			problem(w, http.StatusBadRequest, "Invalid push event", err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]int{"queued": created})
		return
	}
	event, err := events.ParseGitHubEvent(r.Header.Get("X-GitHub-Event"), r.Header.Get("X-GitHub-Delivery"), body, time.Now().UTC())
	if errors.Is(err, events.ErrEventUnsupported) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid webhook event", err.Error())
		return
	}
	event.ProviderConnectionID = connectionID
	groupRuns, err := groupService.Process(r.Context(), event)
	if err != nil {
		problem(w, http.StatusUnprocessableEntity, "Preview group event rejected", err.Error())
		return
	}
	result, err := eventService.Process(r.Context(), event)
	if err != nil {
		a.internal(w, err)
		return
	}
	result.PreviewGroupRuns = groupRuns
	status := http.StatusAccepted
	if result.Duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, result)
}

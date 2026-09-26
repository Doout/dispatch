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
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) listEventTriggers(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListEventTriggers(r.Context(), strings.TrimSpace(r.URL.Query().Get("appId")))
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleAppIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	filtered := items[:0]
	member := currentIdentity(r.Context()).SystemRole != core.UserRoleOwner
	for _, item := range items {
		if visible[item.AppID] {
			if member {
				item = redactEventTriggerCredentials(item)
			}
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

type createEventTriggerRequest struct {
	Provider       string   `json:"provider"`
	GitHubAppID    *string  `json:"githubAppId"`
	Repository     string   `json:"repository"`
	Command        string   `json:"command"`
	Enabled        *bool    `json:"enabled"`
	PreDeployHook  string   `json:"preDeployHook"`
	PostDeployHook string   `json:"postDeployHook"`
	SecretIDs      []string `json:"secretIds"`
}

func (a *API) validateSecretIDs(ctx context.Context, ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return errors.New("secret bindings must be unique")
		}
		seen[id] = true
		if _, err := a.store.GetSecret(ctx, id); err != nil {
			return errors.New("one or more selected secrets no longer exist")
		}
	}
	return nil
}

const maxEventHookBytes = 64 << 10

func validateEventHooks(preDeployHook, postDeployHook string) error {
	if len(preDeployHook) > maxEventHookBytes || len(postDeployHook) > maxEventHookBytes {
		return fmt.Errorf("each deployment hook must be no larger than 64 KiB")
	}
	if strings.ContainsRune(preDeployHook+postDeployHook, 0) {
		return errors.New("deployment hooks must contain plain shell text")
	}
	return nil
}

func (a *API) createEventTrigger(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	if _, err := a.store.GetApp(r.Context(), appID); err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	var input createEventTriggerRequest
	if !decode(w, r, &input) {
		return
	}
	if !a.requireCredentialOwner(w, r, len(input.SecretIDs) > 0 || input.GitHubAppID != nil && strings.TrimSpace(*input.GitHubAppID) != "") {
		return
	}
	if err := validateEventHooks(input.PreDeployHook, input.PostDeployHook); err != nil {
		problem(w, http.StatusBadRequest, "Invalid deployment hook", err.Error())
		return
	}
	if err := a.validateSecretIDs(r.Context(), input.SecretIDs); err != nil {
		problem(w, http.StatusBadRequest, "Invalid secret binding", err.Error())
		return
	}
	providerName := strings.ToLower(strings.TrimSpace(input.Provider))
	if providerName == "" {
		providerName = string(core.EventProviderGitHub)
	}
	if providerName != string(core.EventProviderGitHub) {
		problem(w, http.StatusBadRequest, "Event provider unavailable", "Use the github provider for pull request preview events.")
		return
	}
	repository := events.NormalizeRepository(input.Repository)
	if owner, name, ok := strings.Cut(repository, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		problem(w, http.StatusBadRequest, "Repository required", "Use an owner/repository identifier.")
		return
	}
	githubAppID := ""
	if input.GitHubAppID != nil {
		githubAppID = strings.TrimSpace(*input.GitHubAppID)
	}
	if githubAppID != "" {
		if err := a.validateGitHubRepositoryAccess(r.Context(), githubAppID, repository); err != nil {
			problem(w, http.StatusBadRequest, "Repository is not connected", err.Error())
			return
		}
	}
	command, err := events.NormalizeCommand(input.Command, a.eventConfig.DefaultCommand)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid trigger command", err.Error())
		return
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC()
	requested := core.EventTrigger{ID: ulid.Make().String(), AppID: appID, GitHubAppID: githubAppID, Provider: core.EventProvider(providerName),
		Repository: repository, Command: command, Enabled: enabled, PreDeployHook: input.PreDeployHook,
		PostDeployHook: input.PostDeployHook, SecretIDs: input.SecretIDs, CreatedAt: now, UpdatedAt: now}
	item, created, err := a.store.CreateEventTrigger(r.Context(), requested)
	if err != nil {
		a.internal(w, err)
		return
	}
	if !created && (item.GitHubAppID != requested.GitHubAppID || item.Command != requested.Command || item.Enabled != requested.Enabled || item.PreDeployHook != requested.PreDeployHook || item.PostDeployHook != requested.PostDeployHook || !slices.Equal(item.SecretIDs, requested.SecretIDs)) {
		problem(w, http.StatusConflict, "Event trigger already exists", "Update the existing event rule to change its command, state, or deployment hooks.")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, item)
}

func (a *API) updateEventTrigger(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	items, err := a.store.ListEventTriggers(r.Context(), "")
	if err != nil {
		a.internal(w, err)
		return
	}
	var item *core.EventTrigger
	for index := range items {
		if items[index].ID == id {
			item = &items[index]
			break
		}
	}
	if item == nil {
		problem(w, http.StatusNotFound, "Event trigger not found", "The requested event trigger does not exist.")
		return
	}
	var input createEventTriggerRequest
	if !decode(w, r, &input) {
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner {
		if len(input.SecretIDs) > 0 || input.GitHubAppID != nil && strings.TrimSpace(*input.GitHubAppID) != "" {
			problem(w, http.StatusForbidden, "Credential access denied", "A controller owner must change event credentials.")
			return
		}
		input.SecretIDs = append([]string(nil), item.SecretIDs...)
		input.GitHubAppID = nil
	}
	if err := validateEventHooks(input.PreDeployHook, input.PostDeployHook); err != nil {
		problem(w, http.StatusBadRequest, "Invalid deployment hook", err.Error())
		return
	}
	if err := a.validateSecretIDs(r.Context(), input.SecretIDs); err != nil {
		problem(w, http.StatusBadRequest, "Invalid secret binding", err.Error())
		return
	}
	command, err := events.NormalizeCommand(input.Command, item.Command)
	if err != nil {
		problem(w, http.StatusBadRequest, "Invalid trigger command", err.Error())
		return
	}
	if input.GitHubAppID != nil {
		githubAppID := strings.TrimSpace(*input.GitHubAppID)
		if githubAppID != "" {
			if err := a.validateGitHubRepositoryAccess(r.Context(), githubAppID, item.Repository); err != nil {
				problem(w, http.StatusBadRequest, "Repository is not connected", err.Error())
				return
			}
		}
		item.GitHubAppID = githubAppID
	}
	enabled := item.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	item.Command = command
	item.Enabled = enabled
	item.PreDeployHook = input.PreDeployHook
	item.PostDeployHook = input.PostDeployHook
	item.SecretIDs = input.SecretIDs
	item.UpdatedAt = time.Now().UTC()
	if err := a.store.UpdateEventTrigger(r.Context(), *item); err != nil {
		a.notFoundOrInternal(w, err, "Event trigger")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteEventTrigger(w http.ResponseWriter, r *http.Request) {
	if err := a.store.DeleteEventTrigger(r.Context(), chi.URLParam(r, "id")); err != nil {
		a.notFoundOrInternal(w, err, "Event trigger")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) listPreviewEnvironments(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListPreviewEnvironments(r.Context(), strings.TrimSpace(r.URL.Query().Get("appId")))
	if err != nil {
		a.internal(w, err)
		return
	}
	visible, err := a.visibleAppIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	filtered := items[:0]
	for _, item := range items {
		if visible[item.AppID] {
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

const maxWebhookBytes = 1 << 20

func (a *API) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.WebhookSecret == "" {
		problem(w, http.StatusServiceUnavailable, "Webhook receiver is not configured", "Set a webhook secret before sending events.")
		return
	}
	a.processGitHubWebhook(w, r, a.eventConfig.WebhookSecret, "", 0, a.groups, a.events)
}

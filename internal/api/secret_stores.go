package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type secretStoreRequest struct {
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	ServiceURL       string `json:"serviceUrl"`
	IAMURL           string `json:"iamUrl"`
	APIKey           string `json:"apiKey"`
	PrivateNetworkID string `json:"privateNetworkId"`
	ServiceAddress   string `json:"serviceAddress"`
	IAMAddress       string `json:"iamAddress"`
}

func (a *API) listSecretStores(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListSecretStores(r.Context())
	a.list(w, items, err)
}

func (a *API) createSecretStore(w http.ResponseWriter, r *http.Request) {
	if a.secretResolver == nil || a.eventConfig.Vault == nil {
		problem(w, http.StatusServiceUnavailable, "Secret storage is not configured", "Set DISPATCH_MASTER_KEY_FILE before adding a secret store.")
		return
	}
	var input secretStoreRequest
	if !decode(w, r, &input) {
		return
	}
	item, credentials, detail := secretStoreInput(input, nil)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Invalid secret store", detail)
		return
	}
	item.ID = ulid.Make().String()
	item.CreatedAt, item.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	if err := a.verifySecretStoreInput(r.Context(), item, credentials); err != nil {
		problem(w, http.StatusBadRequest, "Connection failed", err.Error())
		return
	}
	encoded, _ := json.Marshal(credentials)
	encrypted, err := a.eventConfig.Vault.Encrypt("secret-store:"+item.ID, encoded)
	clear(encoded)
	if err != nil {
		a.internal(w, err)
		return
	}
	item.EncryptedCredentials = encrypted
	item.CredentialsConfigured = true
	item.State = "ready"
	verified := time.Now().UTC()
	item.LastVerifiedAt = &verified
	if err := a.store.CreateSecretStore(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *API) updateSecretStore(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetSecretStore(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Secret store")
		return
	}
	var input secretStoreRequest
	if !decode(w, r, &input) {
		return
	}
	updated, credentials, detail := secretStoreInput(input, &item)
	if detail != "" {
		problem(w, http.StatusBadRequest, "Invalid secret store", detail)
		return
	}
	updated.ID, updated.CreatedAt, updated.EncryptedCredentials = item.ID, item.CreatedAt, item.EncryptedCredentials
	updated.UpdatedAt = time.Now().UTC()
	if strings.TrimSpace(input.APIKey) != "" {
		if err := a.verifySecretStoreInput(r.Context(), updated, credentials); err != nil {
			problem(w, http.StatusBadRequest, "Connection failed", err.Error())
			return
		}
		encoded, _ := json.Marshal(credentials)
		updated.EncryptedCredentials, err = a.eventConfig.Vault.Encrypt("secret-store:"+updated.ID, encoded)
		clear(encoded)
		if err != nil {
			a.internal(w, err)
			return
		}
		verified := time.Now().UTC()
		updated.State, updated.LastVerifiedAt = "ready", &verified
	} else {
		if err := a.secretResolver.VerifyStored(r.Context(), updated); err != nil {
			problem(w, http.StatusBadRequest, "Connection failed", err.Error())
			return
		}
		verified := time.Now().UTC()
		updated.State, updated.LastVerifiedAt = "ready", &verified
	}
	updated.CredentialsConfigured = updated.EncryptedCredentials != ""
	if err := a.store.UpdateSecretStore(r.Context(), updated); err != nil {
		a.notFoundOrInternal(w, err, "Secret store")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) verifySecretStore(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetSecretStore(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Secret store")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := a.secretResolver.VerifyStored(ctx, item); err != nil {
		item.State, item.UpdatedAt = "error", time.Now().UTC()
		_ = a.store.UpdateSecretStore(r.Context(), item)
		problem(w, http.StatusBadGateway, "Connection failed", err.Error())
		return
	}
	verified := time.Now().UTC()
	item.State, item.LastVerifiedAt, item.UpdatedAt = "ready", &verified, verified
	if err := a.store.UpdateSecretStore(r.Context(), item); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) deleteSecretStore(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	items, err := a.store.ListSecrets(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, item := range items {
		if item.ExternalStoreID == id {
			problem(w, http.StatusConflict, "Secret store in use", "Remove its external secret references first.")
			return
		}
	}
	if err := a.store.DeleteSecretStore(r.Context(), id); err != nil {
		a.notFoundOrInternal(w, err, "Secret store")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func secretStoreInput(input secretStoreRequest, existing *core.SecretStore) (core.SecretStore, map[string]string, string) {
	input.Name, input.Provider = strings.TrimSpace(input.Name), strings.TrimSpace(input.Provider)
	input.ServiceURL, input.IAMURL = strings.TrimRight(strings.TrimSpace(input.ServiceURL), "/"), strings.TrimRight(strings.TrimSpace(input.IAMURL), "/")
	if input.Provider == "" {
		input.Provider = secretvalue.ProviderIBMCloudSecretsManager
	}
	if input.IAMURL == "" {
		input.IAMURL = "https://iam.cloud.ibm.com"
	}
	item := core.SecretStore{Name: input.Name, Provider: input.Provider, Config: map[string]string{
		"serviceUrl": input.ServiceURL, "iamUrl": input.IAMURL,
		"privateNetworkId": strings.TrimSpace(input.PrivateNetworkID), "serviceAddress": strings.TrimSpace(input.ServiceAddress), "iamAddress": strings.TrimSpace(input.IAMAddress),
	}}
	if input.Name == "" || len(input.Name) > 80 {
		return item, nil, "Enter a name no longer than 80 characters."
	}
	if err := secretvalue.ValidateStore(item); err != nil {
		return item, nil, err.Error()
	}
	credentials := map[string]string{"apiKey": input.APIKey}
	if existing == nil && strings.TrimSpace(input.APIKey) == "" {
		return item, credentials, "Enter an IBM Cloud API key."
	}
	return item, credentials, ""
}

func (a *API) verifySecretStoreInput(ctx context.Context, item core.SecretStore, credentials map[string]string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return a.secretResolver.Verify(ctx, item, credentials)
}

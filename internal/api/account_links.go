package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

type accountAuthLink struct {
	Provider  publicAuthProvider     `json:"provider"`
	Identity  *core.ExternalIdentity `json:"identity,omitempty"`
	Available bool                   `json:"available"`
}

func (a *API) accountAuthLinks(w http.ResponseWriter, r *http.Request) {
	identity := currentIdentity(r.Context())
	if identity.ID == "" || identity.ID == "controller-owner" {
		problem(w, http.StatusConflict, "Account linking unavailable", "Create a stored user before linking an external account.")
		return
	}
	result, err := a.accountLinksForUser(r.Context(), identity.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) accountLinksForUser(ctx context.Context, userID string) ([]accountAuthLink, error) {
	providers, err := a.store.ListAuthProviders(ctx)
	if err != nil {
		return nil, err
	}
	identities, err := a.store.ListExternalIdentities(ctx)
	if err != nil {
		return nil, err
	}
	linked := map[string]core.ExternalIdentity{}
	for _, item := range identities {
		if item.UserID == userID {
			linked[item.ProviderID] = item
		}
	}
	result := []accountAuthLink{}
	for _, provider := range providers {
		external, hasIdentity := linked[provider.ID]
		available := provider.State == core.AuthProviderStateReady
		if !available && !hasIdentity {
			continue
		}
		item := accountAuthLink{Provider: publicProvider(provider), Available: available}
		if hasIdentity {
			copy := external
			item.Identity = &copy
		}
		result = append(result, item)
	}
	return result, nil
}

func (a *API) startOAuthLink(w http.ResponseWriter, r *http.Request) {
	identity := currentIdentity(r.Context())
	if identity.ID == "" || identity.ID == "controller-owner" {
		problem(w, http.StatusConflict, "Account linking unavailable", "Create a stored user before linking an external account.")
		return
	}
	var input struct {
		ReturnTo string `json:"returnTo"`
	}
	if !decode(w, r, &input) {
		return
	}
	a.beginOAuth(w, r, identity.ID, input.ReturnTo)
}

func (a *API) unlinkAuthProvider(w http.ResponseWriter, r *http.Request) {
	identity := currentIdentity(r.Context())
	if identity.ID == "" || identity.ID == "controller-owner" {
		problem(w, http.StatusConflict, "Account linking unavailable", "This account is managed by the controller configuration.")
		return
	}
	providerID := strings.TrimSpace(chi.URLParam(r, "id"))
	identities, err := a.store.ListExternalIdentities(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	linked := []core.ExternalIdentity{}
	found := false
	for _, item := range identities {
		if item.UserID != identity.ID {
			continue
		}
		linked = append(linked, item)
		if item.ProviderID == providerID {
			found = true
		}
	}
	if !found {
		problem(w, http.StatusNotFound, "Linked account not found", "This sign-in method is not linked to your user.")
		return
	}
	user, err := a.store.GetUser(r.Context(), identity.ID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "Your account no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if user.PasswordHash == "" && len(linked) <= 1 {
		problem(w, http.StatusConflict, "Sign-in method required", "Link another account before removing your only sign-in method.")
		return
	}
	if err := a.store.DeleteExternalIdentity(r.Context(), providerID, identity.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			problem(w, http.StatusNotFound, "Linked account not found", "This sign-in method is not linked to your user.")
			return
		}
		a.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

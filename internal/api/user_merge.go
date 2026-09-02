package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) mergeUser(w http.ResponseWriter, r *http.Request) {
	sourceID := strings.TrimSpace(chi.URLParam(r, "id"))
	var input struct {
		TargetUserID string `json:"targetUserId"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.TargetUserID = strings.TrimSpace(input.TargetUserID)
	if sourceID == "" || input.TargetUserID == "" || sourceID == input.TargetUserID {
		problem(w, http.StatusBadRequest, "Choose another user", "Select the user that should remain after the merge.")
		return
	}
	if sourceID == currentIdentity(r.Context()).ID {
		problem(w, http.StatusConflict, "Cannot merge current user", "Sign in as the user you want to keep, or use another owner account.")
		return
	}
	source, err := a.store.GetUser(r.Context(), sourceID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "The user being merged no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	target, err := a.store.GetUser(r.Context(), input.TargetUserID)
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "User not found", "The user being kept no longer exists.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if target.State != core.UserStateActive {
		problem(w, http.StatusConflict, "Active user required", "Keep an active user when merging accounts.")
		return
	}
	if source.SystemRole == core.UserRoleOwner && target.SystemRole != core.UserRoleOwner {
		problem(w, http.StatusConflict, "Owner role would be lost", "Keep the owner account when merging these users.")
		return
	}
	if err := a.store.MergeUsers(r.Context(), source.ID, target.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrIdentityConflict):
			problem(w, http.StatusConflict, "Authentication type conflict", "Both users use the same sign-in type. Unlink one account before merging.")
		case errors.Is(err, store.ErrNotFound):
			problem(w, http.StatusNotFound, "User not found", "One of these users no longer exists.")
		default:
			a.internal(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, target)
}

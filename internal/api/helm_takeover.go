package api

import (
	"errors"
	"net/http"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) helmTakeover(w http.ResponseWriter, r *http.Request) {
	if a.auth.Hosted != nil {
		problem(w, 422, "Takeover unavailable", "Open the self-hosted controller that manages this Kubernetes target.")
		return
	}
	ctx := r.Context()
	app, err := a.store.GetApp(ctx, chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Application")
		return
	}
	if app.Template || app.BuildType != core.BuildTypeHelm {
		problem(w, 422, "Takeover unavailable", "Select a deployed Helm application target.")
		return
	}
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Server")
		return
	}
	var input struct {
		Version                   string `json:"version"`
		PreviousControllerStopped bool   `json:"previousControllerStopped"`
	}
	if r.Method == http.MethodPost {
		if !decode(w, r, &input) {
			return
		}
		if len(input.Version) != 64 || !input.PreviousControllerStopped {
			problem(w, 400, "Review required", "Inspect the release and confirm the previous controller has stopped managing it.")
			return
		}
	}
	review, err := deploy.TransferHelmRelease(ctx, app, server, input.Version, func(id string) error {
		_, err := a.store.GetApp(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return errors.New("Could not verify the previous application in this controller.")
		}
		return errors.New("The previous application still exists on this controller. Resolve the release name conflict with its owner.")
	})
	if err != nil {
		problem(w, http.StatusConflict, "Helm takeover unavailable", err.Error())
		return
	}
	if r.Method == http.MethodPost {
		if audit, ok := ctx.Value(destructiveAuditKey{}).(*destructiveAudit); ok {
			audit.Review = &destructiveReview{ResourceID: app.ID, ResourceType: "application", ProjectID: app.ProjectID, AppID: app.ID, Name: review.Namespace + "/" + review.Release, Action: "helm_takeover", Version: review.Version}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, review)
}

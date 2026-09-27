package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
)

func (a *API) listDeployments(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	items, err := a.store.ListDeployments(r.Context(), limit)
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
			if member && item.App != nil {
				redacted := redactAppCredentials(*item.App)
				item.App = &redacted
			}
			filtered = append(filtered, item)
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (a *API) startDeployment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		CommitSHA string                 `json:"commitSha"`
		Review    *core.DeploymentReview `json:"review,omitempty"`
	}
	if r.ContentLength > 0 && !decode(w, r, &input) {
		return
	}
	var item core.Deployment
	var err error
	if input.Review != nil {
		if input.Review.ExpectedAppName == "" || input.Review.ProjectID == "" || input.Review.AppSpecDigest == "" || input.Review.BindingsDigest == "" || input.Review.ServiceRevisions == nil {
			problem(w, 422, "Incomplete deployment review", "Preview the application again before deploying reviewed inputs.")
			return
		}
		item, err = a.deploy.StartReviewed(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(input.CommitSHA), *input.Review)
	} else {
		item, err = a.deploy.Start(r.Context(), chi.URLParam(r, "id"), strings.TrimSpace(input.CommitSHA))
	}
	if errors.Is(err, store.ErrDeploymentReviewChanged) {
		problem(w, 409, "Deployment inputs changed", "Application settings, service bindings, or service revisions changed after preview. Review the current inputs before deploying.")
		return
	}
	if errors.Is(err, deploy.ErrApplicationTemplate) {
		problem(w, http.StatusConflict, "Template cannot be deployed", "Use this template from an event rule or preview group.")
		return
	}
	if errors.Is(err, deploy.ErrDeploymentActive) {
		problem(w, http.StatusConflict, "Deployment already active", err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Application not found", "Refresh the application inventory and try again.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (a *API) getDeployment(w http.ResponseWriter, r *http.Request) {
	item, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, store.ErrNotFound) {
		problem(w, http.StatusNotFound, "Deployment not found", "The deployment record does not exist.")
		return
	}
	if err != nil {
		a.internal(w, err)
		return
	}
	if currentIdentity(r.Context()).SystemRole != core.UserRoleOwner && item.App != nil {
		redacted := redactAppCredentials(*item.App)
		item.App = &redacted
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *API) getDeploymentLogs(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	items, err := a.store.ListDeploymentLogs(r.Context(), chi.URLParam(r, "id"), after)
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (a *API) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	if err := a.deploy.Cancel(r.Context(), chi.URLParam(r, "id")); err != nil {
		problem(w, http.StatusConflict, "Deployment cannot be cancelled", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deploymentEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, http.StatusInternalServerError, "Streaming unavailable", "The HTTP server does not support streaming.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	lastID := int64(0)
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		logs, err := a.store.ListDeploymentLogs(r.Context(), chi.URLParam(r, "id"), lastID)
		if err != nil {
			return
		}
		for _, entry := range logs {
			lastID = entry.ID
			payload, _ := json.Marshal(entry)
			fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", entry.ID, payload)
		}
		deployment, err := a.store.GetDeployment(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			return
		}
		payload, _ := json.Marshal(deployment)
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", payload)
		flusher.Flush()
		if deployment.State.Terminal() && len(logs) == 0 {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

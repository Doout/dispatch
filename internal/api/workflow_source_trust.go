package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

func (a *API) previewSourceTrustReview(w http.ResponseWriter, r *http.Request) {
	revision, err := a.store.GetWorkflowRevision(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow revision")
		return
	}
	resource, err := a.store.GetWorkflowResource(r.Context(), revision.ResourceID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow resource")
		return
	}
	decision, err := a.workflows.SourceTrust(r.Context(), resource, revision)
	if err != nil && !errors.Is(err, workflow.ErrPreviewSourceTrust) {
		a.internal(w, err)
		return
	}
	if decision == nil {
		if err != nil {
			a.internal(w, err)
		} else {
			problem(w, 409, "No preview source policy", "This run is not associated with a PR preview.")
		}
		return
	}
	// A denied decision is a successful read of the review, not approval.
	data, ok := a.store.(store.PreviewSourceTrustStore)
	if !ok {
		problem(w, 503, "Source trust unavailable", "Source trust storage is unavailable.")
		return
	}
	if err := data.UpdatePreviewSourceTrustDecision(r.Context(), revision.ID, decision); err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, decision)
}

func (a *API) approvePreviewSourceTrust(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ConfirmDigest string    `json:"confirmDigest"`
		ExpiresAt     time.Time `json:"expiresAt"`
	}
	if !decode(w, r, &input) {
		return
	}
	now := time.Now().UTC()
	if input.ConfirmDigest == "" || !input.ExpiresAt.After(now) || input.ExpiresAt.After(now.Add(24*time.Hour)) {
		problem(w, 422, "Invalid source approval", "Confirm the reviewed digest and choose an expiry within the next 24 hours.")
		return
	}
	revision, err := a.store.GetWorkflowRevision(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow revision")
		return
	}
	resource, err := a.store.GetWorkflowResource(r.Context(), revision.ResourceID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow resource")
		return
	}
	decision, evaluationErr := a.workflows.SourceTrust(r.Context(), resource, revision)
	if evaluationErr != nil && !errors.Is(evaluationErr, workflow.ErrPreviewSourceTrust) {
		a.internal(w, evaluationErr)
		return
	}
	if decision == nil || decision.Digest == "" || decision.Digest != input.ConfirmDigest {
		problem(w, 409, "Preview source review changed", "Review the current source identities, credential scope and environments before approving.")
		return
	}
	data, ok := a.store.(store.PreviewSourceTrustStore)
	if !ok {
		problem(w, 503, "Source trust unavailable", "Source trust storage is unavailable.")
		return
	}
	approval := core.PreviewSourceTrustApproval{ID: ulid.Make().String(), ResourceID: resource.ID, Digest: decision.Digest, ActorID: currentIdentity(r.Context()).ID, ExpiresAt: input.ExpiresAt.UTC(), CreatedAt: now}
	if err := data.CreatePreviewSourceTrustApproval(r.Context(), approval); err != nil {
		a.internal(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, approval)
}

func (a *API) revokePreviewSourceTrust(w http.ResponseWriter, r *http.Request) {
	revision, err := a.store.GetWorkflowRevision(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow revision")
		return
	}
	data, ok := a.store.(store.PreviewSourceTrustStore)
	if !ok {
		problem(w, 503, "Source trust unavailable", "Source trust storage is unavailable.")
		return
	}
	if err := data.RevokePreviewSourceTrustApproval(r.Context(), revision.ResourceID, chi.URLParam(r, "approvalId")); err != nil {
		a.notFoundOrInternal(w, err, "Source approval")
		return
	}
	w.WriteHeader(204)
}

func (a *API) resolvePreviewSource(ctx context.Context, appID, repository string, number int) (githubapp.PullRequestHead, error) {
	if appID != "" && a.eventConfig.GitHubApps != nil {
		return a.eventConfig.GitHubApps.PullRequestHead(ctx, appID, repository, number)
	}
	if a.eventConfig.GitHubToken == "" {
		return githubapp.PullRequestHead{}, errors.New("GitHub source verification unavailable")
	}
	resolver := events.GitHubResolver{BaseURL: a.eventConfig.GitHubAPIURL, Token: a.eventConfig.GitHubToken}
	pr, err := resolver.ResolvePullRequest(ctx, repository, number)
	if err != nil {
		return githubapp.PullRequestHead{}, err
	}
	head := githubapp.PullRequestHead{}
	head.Head.SHA = pr.HeadSHA
	head.Head.Repo = &githubapp.PullRequestRepository{ID: pr.HeadRepositoryID, FullName: pr.HeadRepository}
	head.Base.Repo = &githubapp.PullRequestRepository{ID: pr.RepositoryID, FullName: pr.Repository}
	if pr.Open {
		head.State = "open"
	} else {
		head.State = "closed"
	}
	return head, nil
}

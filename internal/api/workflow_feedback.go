package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"

	"github.com/oklog/ulid/v2"
)

// Reporting progress is stored independently of comment delivery and worker
// state. Polling retries it after GitHub outages or controller restarts.
func (a *API) reportWorkflowFeedback(ctx context.Context, resourceID string) error {
	holder := ulid.Make().String()
	now := time.Now().UTC()
	leased, err := a.store.AcquireWorkflowFeedbackLease(ctx, resourceID, holder, now, now.Add(2*time.Minute))
	if err != nil {
		return err
	}
	if !leased {
		return nil
	}
	defer a.store.ReleaseWorkflowFeedbackLease(context.Background(), resourceID, holder)
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	pending, err := a.store.PendingWorkflowFeedback(ctx, resourceID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	history, err := a.store.ListWorkflowRevisions(ctx, resourceID, 0)
	if err != nil {
		return err
	}
	var failures []error
	for _, revision := range pending {
		feedback := revision.Feedback
		terminal := revision.State == "succeeded" || revision.State == "failed" || revision.State == "cancelled"
		outdated := false
		for _, newer := range history {
			if !workflowRevisionNewer(newer, revision) {
				continue
			}
			// New QA commands supersede older feedback. A newer deployment also
			// invalidates the instance used by the completed check.
			outdated = true
			break
		}
		if outdated {
			for i := range feedback.Targets {
				if feedback.Targets[i].ReviewID == 0 {
					feedback.Targets[i].Review = "skipped"
				}
				feedback.Targets[i].SkipReason = "A newer preview or QA run replaced this result."
				feedback.Targets[i].Error = ""
			}
			feedback.Complete = true
			// A replacement QA run on the same commits owns the status context.
			// On other commits, settle an existing pending status as an error.
			for i := range feedback.Targets {
				target := &feedback.Targets[i]
				replaced := false
				for _, newer := range history {
					if !workflowRevisionNewer(newer, revision) || newer.Feedback == nil || newer.Feedback.StatusContext != feedback.StatusContext {
						continue
					}
					for _, next := range newer.Feedback.Targets {
						if next.Repository == target.Repository && next.CommitSHA == target.CommitSHA {
							replaced = true
						}
					}
				}
				if !replaced && target.Status == "pending" {
					if err := a.publishWorkflowFeedbackStatus(ctx, revision, *target, "error"); err != nil {
						target.Error = err.Error()
						feedback.Complete = false
						failures = append(failures, err)
					} else {
						target.Status = "error"
					}
				}
			}
		} else {
			state := "pending"
			switch revision.State {
			case "succeeded":
				state = "success"
			case "failed":
				state = "failure"
			case "cancelled":
				state = "error"
			}
			// Reviews require every linked PR to still match the tested source set.
			reviewSkip := ""
			var reviewLookupError error
			reviewEvent := ""
			if revision.State == "succeeded" && feedback.ReviewOnSuccess == "approve" {
				reviewEvent = "APPROVE"
			}
			if revision.State == "failed" && feedback.ReviewOnFailure == "requestChanges" {
				reviewEvent = "REQUEST_CHANGES"
			}
			if reviewEvent != "" {
				for _, target := range feedback.Targets {
					if target.Review == "skipped" {
						reviewSkip = target.SkipReason
						break
					}
					head, err := a.eventConfig.GitHubApps.PullRequestHead(ctx, target.GitHubAppID, target.Repository, target.Number)
					if err != nil {
						reviewLookupError = err
						break
					}
					if head.State != "open" || head.Draft || head.Head.SHA != target.CommitSHA {
						reviewSkip = "A linked PR is closed, draft, or has commits newer than the tested deployment."
						break
					}
				}
			}
			feedback.Complete = false
			complete := terminal
			for i := range feedback.Targets {
				target := &feedback.Targets[i]
				target.Error = ""
				if target.Status != state {
					if err := a.publishWorkflowFeedbackStatus(ctx, revision, *target, state); err != nil {
						target.Error = err.Error()
						complete = false
						failures = append(failures, err)
						continue
					}
					target.Status = state
					// Save each successful write before the next network request.
					if err := a.store.UpdateWorkflowFeedback(ctx, revision.ID, feedback); err != nil {
						return err
					}
				}
				if !terminal {
					continue
				}
				if target.ReviewID > 0 {
					continue
				}
				if reviewLookupError != nil {
					target.Error = reviewLookupError.Error()
					complete = false
					failures = append(failures, reviewLookupError)
					continue
				}
				if reviewEvent == "" {
					target.Review = "disabled"
					continue
				}
				if reviewSkip != "" {
					target.Review = "skipped"
					target.SkipReason = reviewSkip
					continue
				}
				marker := "<!-- dispatch-preview-review:" + revision.ID + " -->"
				body := fmt.Sprintf("Preview QA %s for commit `%s`.\n\n[Tested preview](%s)", revision.State, target.CommitSHA, feedback.PreviewURL)
				id, err := a.eventConfig.GitHubApps.SubmitPullRequestReview(ctx, target.GitHubAppID, target.Repository, target.Number, target.CommitSHA, reviewEvent, marker, body)
				if errors.Is(err, githubapp.ErrReviewOutdated) {
					target.Review = "skipped"
					target.SkipReason = err.Error()
					reviewSkip = err.Error()
					continue
				}
				if err != nil {
					target.Error = err.Error()
					complete = false
					failures = append(failures, err)
					continue
				}
				target.ReviewID = id
				target.Review = "approved"
				if reviewEvent == "REQUEST_CHANGES" {
					target.Review = "changes_requested"
				}
				if err := a.store.UpdateWorkflowFeedback(ctx, revision.ID, feedback); err != nil {
					return err
				}
			}
			feedback.Complete = complete
		}
		if err := a.store.UpdateWorkflowFeedback(ctx, revision.ID, feedback); err != nil {
			return err
		}
	}
	return errors.Join(failures...)
}

func (a *API) publishWorkflowFeedbackStatus(ctx context.Context, revision core.WorkflowRevision, target core.WorkflowFeedbackTarget, state string) error {
	description := map[string]string{"pending": "Preview QA queued or running", "success": "Preview QA passed", "failure": "Preview QA failed", "error": "Preview QA cancelled or superseded"}[state]
	targetURL := strings.TrimRight(a.auth.PublicURL, "/") + "/events?run=" + revision.ID
	if a.auth.PublicURL == "" {
		targetURL = revision.Feedback.PreviewURL
	}
	return a.eventConfig.GitHubApps.SetCommitStatusWithContext(ctx, target.GitHubAppID, target.Repository, target.CommitSHA, state, revision.Feedback.StatusContext, description, targetURL)
}

func workflowRevisionNewer(a, b core.WorkflowRevision) bool {
	return a.CreatedAt.After(b.CreatedAt) || a.CreatedAt.Equal(b.CreatedAt) && a.ID > b.ID
}

func (a *API) reconcileWorkflowFeedback(ctx context.Context) error {
	if a.eventConfig.GitHubApps == nil {
		return nil
	}
	a.previewReportMu.Lock()
	defer a.previewReportMu.Unlock()
	pending, err := a.store.PendingWorkflowFeedback(ctx, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var failures []error
	for _, revision := range pending {
		if seen[revision.ResourceID] {
			continue
		}
		seen[revision.ResourceID] = true
		if err := a.reportWorkflowFeedback(ctx, revision.ResourceID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/runlog"
	"github.com/doout/dispatch/internal/store"
)

// Reporting has its own durable queue. A GitHub failure never changes execution.
func (a *API) RunWorkflowChecks(ctx context.Context) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	lastPrune := time.Time{}
	for {
		if time.Since(lastPrune) > time.Hour {
			_ = runlog.Prune(a.workflows.LogDirectory, time.Now())
			lastPrune = time.Now()
		}
		if err := a.ProcessWorkflowChecksOnce(ctx); err != nil && ctx.Err() == nil && a.logger != nil {
			a.logger.Warn("GitHub check reporting pending")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *API) ProcessWorkflowChecksOnce(ctx context.Context) error {
	if a.eventConfig.GitHubApps == nil {
		return nil
	}
	for i := 0; i < 50; i++ {
		report, err := a.store.ClaimWorkflowCheck(ctx, time.Now().UTC(), 2*time.Minute)
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, 90*time.Second)
		err = a.reportWorkflowCheck(attempt, report)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *API) reportWorkflowCheck(ctx context.Context, r core.WorkflowCheckReport) error {
	revision, err := a.store.GetWorkflowRevision(ctx, r.RevisionID)
	var payload githubapp.CheckRunUpdate
	var logErr error
	if err == nil {
		logErr = a.reconcileGitHubLogs(ctx, &r)
	}
	if err == nil {
		payload, err = a.workflowCheckPayload(ctx, r, revision)
	}
	if err == nil {
		data, _ := json.Marshal(payload)
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		if r.Digest != digest || r.CheckID == 0 {
			r.Attempts++
			var found githubapp.CheckRun
			found, err = a.eventConfig.GitHubApps.FindCheckRun(ctx, r)
			if errors.Is(err, githubapp.ErrCheckRunNotFound) && r.CreateState == "" {
				if err = a.store.BeginWorkflowCheckCreate(ctx, r, time.Now().UTC()); err != nil {
					return err
				}
				r.CreateState = "posting"
				found, err = a.eventConfig.GitHubApps.PublishCheckRun(ctx, r, payload, true)
				if err != nil {
					var failure *githubapp.CheckRunError
					if errors.As(err, &failure) && !failure.CreateUncertain {
						r.CreateState = ""
					}
				}
			} else if err == nil {
				r.CheckID = found.ID
				found, err = a.eventConfig.GitHubApps.PublishCheckRun(ctx, r, payload, false)
			} else if errors.Is(err, githubapp.ErrCheckRunNotFound) {
				err = &githubapp.CheckRunError{Message: "The create response was lost and the accepted check has not appeared yet. Dispatch will keep reconciling its identity without creating a duplicate."}
			}
			if err == nil {
				r.CheckID, r.HTMLURL = found.ID, safeCheckLink(found.HTMLURL)
				r.CreateState = "created"
				r.Digest = digest
			}
		}
	}
	r.NextAttemptAt = time.Now().UTC().Add(5 * time.Second)
	if err == nil && logErr != nil {
		err = logErr
	}
	if err != nil {
		r.State = "retrying"
		r.Error = "Check reporting is temporarily unavailable. Dispatch will retry; the workflow outcome is unchanged."
		var failure *githubapp.CheckRunError
		if errors.As(err, &failure) {
			r.Error = failure.Message
		}
		delay := time.Duration(1<<min(r.Attempts, 8)) * 5 * time.Second
		r.NextAttemptAt = time.Now().UTC().Add(delay)
	} else {
		r.Status, r.Conclusion = payload.Status, payload.Conclusion
		r.Error = ""
		r.State = "reported"
		r.Complete = payload.Status == "completed" && (!r.GitHubLogs || r.LogRunID > 0)
		if r.Complete && (r.Kind == "build" || r.Kind == "stage") {
			checks, loadErr := a.store.ListWorkflowChecks(ctx, r.RevisionID)
			if loadErr != nil {
				return loadErr
			}
			for _, check := range checks {
				if check.Kind == "deployment" && check.Repository == r.Repository && check.CommitSHA == r.CommitSHA && check.GitHubAppID == r.GitHubAppID && check.GitHubLogs && check.LogRunID == 0 {
					r.Complete = false
					break
				}
			}
		}
	}
	return a.store.FinishWorkflowCheck(ctx, r, time.Now().UTC())
}

func safeCheckLink(value string) string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return u.String()
}

func (a *API) workflowCheckPayload(ctx context.Context, r core.WorkflowCheckReport, revision core.WorkflowRevision) (githubapp.CheckRunUpdate, error) {
	state, started, finished := revision.State, revision.StartedAt, revision.FinishedAt
	interrupted := revision.Error == core.WorkflowInterruptedMessage
	skipped := false
	if r.Kind == "build" || r.Kind == "stage" {
		state, started, finished = "queued", nil, nil
		found := false
		if r.Kind == "build" {
			jobs, err := a.store.ListWorkflowJobSummaries(ctx, revision.ID)
			if err != nil {
				return githubapp.CheckRunUpdate{}, err
			}
			for _, job := range jobs {
				if job.JobName == r.Job {
					state, started, finished = job.State, job.StartedAt, job.FinishedAt
					interrupted = job.Error == core.WorkflowInterruptedMessage
					found = true
					break
				}
			}
		} else {
			stages, err := a.store.ListWorkflowStageRuns(ctx, revision.ID)
			if err != nil {
				return githubapp.CheckRunUpdate{}, err
			}
			for _, stage := range stages {
				if stage.StageName == r.Stage {
					state, started, finished = stage.State, stage.StartedAt, stage.FinishedAt
					interrupted = stage.Error == core.WorkflowInterruptedMessage
					found = true
					break
				}
			}
		}
		if terminalWorkflowState(revision.State) && (!found || !terminalWorkflowState(state)) {
			state, finished, skipped = revision.State, revision.FinishedAt, started == nil
			interrupted = revision.Error == core.WorkflowInterruptedMessage
		}
	}
	if r.Kind == "health" {
		stages, err := a.store.ListWorkflowStageRuns(ctx, revision.ID)
		if err != nil {
			return githubapp.CheckRunUpdate{}, err
		}
		state, started, finished = "queued", nil, nil
		found := false
		for _, stage := range stages {
			if stage.StageName != r.Stage {
				continue
			}
			if childID := stage.CheckRuns[r.Check]; childID != "" {
				child, err := a.store.GetWorkflowRevision(ctx, childID)
				if err != nil {
					return githubapp.CheckRunUpdate{}, err
				}
				state, started, finished = child.State, child.StartedAt, child.FinishedAt
				interrupted = child.Error == core.WorkflowInterruptedMessage
				found = true
			}
			break
		}
		if !found && (revision.State == "succeeded" || revision.State == "failed" || revision.State == "cancelled") {
			state, finished = revision.State, revision.FinishedAt
			skipped = true
		}
	}
	payload := githubapp.CheckRunUpdate{Name: r.Name, ExternalID: r.ExternalID, StartedAt: started}
	title := "Deployment"
	if r.Kind == "qa" {
		title = "Preview QA"
	} else if r.Kind == "health" {
		title = "Configured health check"
	} else if r.Kind == "build" {
		title = "Build: " + r.Job
	} else if r.Kind == "stage" {
		title = "Deployment: " + r.Stage
	}
	summary := "Waiting for the recorded workflow result."
	switch state {
	case "succeeded":
		payload.Status = "completed"
		payload.Conclusion = "success"
		summary = "The recorded execution succeeded."
	case "failed":
		payload.Status = "completed"
		payload.Conclusion = "failure"
		summary = "The recorded execution failed. Open the Dispatch run for details."
	case "cancelled":
		payload.Status = "completed"
		payload.Conclusion = "cancelled"
		summary = "The recorded execution was cancelled."
	case "running":
		payload.Status = "in_progress"
		summary = "The recorded execution is running."
	case "awaiting_approval":
		payload.Status = "in_progress"
		summary = "The recorded execution is waiting for approval."
	default:
		payload.Status = "queued"
	}
	if payload.Status == "completed" {
		payload.CompletedAt = finished
		if skipped {
			payload.Conclusion = "skipped"
			summary = "This step did not execute before the workflow ended."
		}
		if interrupted {
			payload.Conclusion = "action_required"
			summary = "Controller recovery interrupted execution. Start a new run for a fresh result."
		}
	}
	// Only fixed text and validated links leave Dispatch. Job logs, provider errors,
	// resource output and secret values never enter the GitHub summary.
	if base := safeCheckLink(strings.TrimRight(a.auth.PublicURL, "/")); base != "" {
		payload.DetailsURL = base + "/events?run=" + url.QueryEscape(revision.ID)
	}
	if r.LogURL != "" {
		payload.DetailsURL = safeCheckLink(r.LogURL)
	} else if r.Kind == "build" || r.Kind == "stage" {
		checks, err := a.store.ListWorkflowChecks(ctx, revision.ID)
		if err != nil {
			return githubapp.CheckRunUpdate{}, err
		}
		for _, check := range checks {
			if check.Kind == "deployment" && check.Repository == r.Repository && check.CommitSHA == r.CommitSHA && check.GitHubAppID == r.GitHubAppID && check.LogURL != "" {
				payload.DetailsURL = safeCheckLink(check.LogURL)
				break
			}
		}
	}
	if r.LogURL != "" || strings.Contains(payload.DetailsURL, "/actions/runs/") {
		summary = strings.ReplaceAll(summary, "Open the Dispatch run for details.", "Open the GitHub run for logs.")
	}
	if preview := safeCheckLink(r.PreviewURL); preview != "" {
		if payload.DetailsURL == "" {
			payload.DetailsURL = preview
		}
		// Angle brackets prevent Markdown path characters from creating extra links.
		summary += "\n\nTested preview: <" + strings.NewReplacer("<", "%3C", ">", "%3E").Replace(preview) + ">"
	}
	payload.Output.Title = title + ": " + payload.Status
	if payload.Conclusion != "" {
		payload.Output.Title = title + ": " + payload.Conclusion
	}
	payload.Output.Summary = summary
	return payload, nil
}

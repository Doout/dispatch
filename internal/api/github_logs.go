package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/logsafe"
	"github.com/doout/dispatch/internal/runlog"
	"github.com/go-chi/chi/v5"
)

func (a *API) reconcileGitHubLogs(ctx context.Context, r *core.WorkflowCheckReport) error {
	if r.Logs == nil || r.LogRunID > 0 {
		return nil
	}
	run, err := a.eventConfig.GitHubApps.FindLogWorkflow(ctx, *r)
	if err == nil {
		r.LogRunID, r.LogURL = run.ID, safeCheckLink(run.URL)
		return nil
	}
	if !errors.Is(err, githubapp.ErrLogWorkflowNotFound) {
		return &githubapp.CheckRunError{Message: "GitHub logs are unavailable. Add the log workflow to the configured branch and grant Actions: write to the GitHub App."}
	}
	if r.LogDispatch != "" {
		return &githubapp.CheckRunError{Message: "Waiting for the accepted GitHub log workflow. No duplicate workflow was started."}
	}
	r.LogDispatch = "posting"
	if err := a.store.SaveWorkflowCheckProgress(ctx, *r, time.Now().UTC()); err != nil {
		return err
	}
	if err := a.eventConfig.GitHubApps.DispatchLogWorkflow(ctx, *r); err != nil {
		var failure *githubapp.CheckRunError
		if errors.As(err, &failure) && !failure.CreateUncertain {
			r.LogDispatch = ""
			return err
		}
		// A timeout may follow an accepted dispatch. Keep the intent and search
		// by report ID instead of starting another billable Actions job.
		return &githubapp.CheckRunError{Message: "Waiting for GitHub to return the log run. Reporting will retry without starting another run."}
	}
	r.LogDispatch = "sent"
	return nil
}

type githubLogStream struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}
type githubLogIndex struct {
	Streams  []githubLogStream `json:"streams"`
	Complete bool              `json:"complete"`
	State    string            `json:"state"`
}
type githubLogPage struct {
	Text       string `json:"text"`
	Next       int64  `json:"next"`
	Complete   bool   `json:"complete"`
	Incomplete bool   `json:"incomplete"`
}

func terminalWorkflowState(state string) bool {
	return state == "succeeded" || state == "failed" || state == "cancelled"
}

// GitHub supplies a signed, short-lived workflow identity. Browser accounts,
// installation IDs and caller-supplied repository names cannot grant access.
func (a *API) githubWorkflowLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		http.Error(w, "GitHub workflow identity required", 401)
		return
	}
	report, err := a.store.GetWorkflowCheck(r.Context(), chi.URLParam(r, "id"))
	if err != nil || report.Logs == nil || a.eventConfig.GitHubApps == nil {
		http.Error(w, "GitHub workflow identity required", 401)
		return
	}
	if report.LogRunID == 0 {
		http.Error(w, "GitHub workflow is being registered", 425)
		return
	}
	if err := a.eventConfig.GitHubApps.VerifyLogWorkflowToken(r.Context(), report, token); err != nil {
		http.Error(w, "GitHub workflow identity rejected", 401)
		return
	}
	revision, err := a.store.GetWorkflowRevision(r.Context(), report.RevisionID)
	if err != nil {
		http.Error(w, "Workflow logs unavailable", 410)
		return
	}
	if revision.FinishedAt != nil && time.Since(*revision.FinishedAt) > runlog.Retention {
		http.Error(w, "Log delivery window expired", 410)
		return
	}
	jobs, err := a.store.ListWorkflowJobSummaries(r.Context(), revision.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	checks, err := a.store.ListWorkflowChecks(r.Context(), revision.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	allowed := map[string]bool{}
	for _, check := range checks {
		if check.Kind == "build" && check.Repository == report.Repository && check.CommitSHA == report.CommitSHA && check.AppID == report.AppID && check.APIURL == report.APIURL {
			allowed[check.Job] = true
		}
	}
	stages, err := a.store.ListWorkflowStageRuns(r.Context(), revision.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	index := githubLogIndex{Streams: []githubLogStream{}, Complete: terminalWorkflowState(revision.State), State: revision.State}
	for _, job := range jobs {
		if allowed[job.JobName] {
			index.Streams = append(index.Streams, githubLogStream{"job-" + job.ID, job.JobName, job.State})
		}
	}
	for _, stage := range stages {
		for _, id := range stage.DeploymentIDs {
			index.Streams = append(index.Streams, githubLogStream{"deployment-" + id, "Deployment / " + stage.StageName, stage.State})
		}
	}
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		writeJSON(w, 200, index)
		return
	}
	valid := false
	for _, s := range index.Streams {
		if s.ID == stream {
			valid = true
			break
		}
	}
	if !valid {
		http.Error(w, "Log stream unavailable", 404)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		http.Error(w, "Invalid log cursor", 400)
		return
	}
	if id, ok := strings.CutPrefix(stream, "job-"); ok {
		for _, job := range jobs {
			if job.ID == id {
				settled := terminalWorkflowState(job.State) && (job.FinishedAt == nil || time.Since(*job.FinishedAt) > 10*time.Second)
				if job.ReusedFromID != "" {
					writeJSON(w, 200, textGitHubLogPage(logsafe.Text("Reused job result "+job.ReusedFromID+"\n"), offset, true))
					return
				}
				text, next, incomplete, err := runlog.Read(a.workflows.LogDirectory, id, offset)
				if errors.Is(err, os.ErrNotExist) {
					writeJSON(w, 200, githubLogPage{Next: offset, Complete: settled, Incomplete: settled})
					return
				}
				if err != nil {
					http.Error(w, "Log archive unavailable", 409)
					return
				}
				archived := runlog.Complete(a.workflows.LogDirectory, id)
				done := terminalWorkflowState(job.State) && (archived || incomplete || settled)
				writeJSON(w, 200, githubLogPage{Text: text, Next: next, Complete: done && len(text) == 0, Incomplete: incomplete || settled && !archived})
				return
			}
		}
	}
	id := strings.TrimPrefix(stream, "deployment-")
	page, err := a.deploymentGitHubLogPage(r.Context(), id, offset, index.Complete)
	if err != nil {
		http.Error(w, "Deployment logs unavailable", 409)
		return
	}
	writeJSON(w, 200, page)
}

func textGitHubLogPage(text string, offset int64, done bool) githubLogPage {
	if offset > int64(len(text)) {
		return githubLogPage{Next: offset, Incomplete: true}
	}
	end := min(offset+64<<10, int64(len(text)))
	for end < int64(len(text)) && end > offset && !utf8.RuneStart(text[end]) {
		end--
	}
	return githubLogPage{Text: text[offset:end], Next: end, Complete: done && end == int64(len(text))}
}

func (a *API) deploymentGitHubLogPage(ctx context.Context, id string, offset int64, done bool) (githubLogPage, error) {
	// Reconstruct the filter's PEM state from the beginning. Pages never resume
	// in the middle of a private key or expose an unfiltered database fragment.
	filter := logsafe.New()
	var after, position int64
	var output strings.Builder
	for {
		logs, err := a.store.ListDeploymentLogs(ctx, id, after)
		if err != nil {
			return githubLogPage{}, err
		}
		for _, entry := range logs {
			after = entry.ID
			for _, line := range strings.Split(entry.Message, "\n") {
				text := filter.Line(line) + "\n"
				next := position + int64(len(text))
				if next > offset {
					start := max(offset-position, 0)
					end := min(int64(len(text)), start+int64((64<<10)-output.Len()))
					for end < int64(len(text)) && end > start && !utf8.RuneStart(text[end]) {
						end--
					}
					output.WriteString(text[start:end])
					if end < int64(len(text)) {
						return githubLogPage{Text: output.String(), Next: position + end}, nil
					}
				}
				position = next
				if output.Len() >= 64<<10 {
					return githubLogPage{Text: output.String(), Next: position}, nil
				}
			}
		}
		if len(logs) < 500 {
			break
		}
	}
	if offset > position {
		return githubLogPage{}, fmt.Errorf("invalid deployment log cursor")
	}
	return githubLogPage{Text: output.String(), Next: position, Complete: done}, nil
}

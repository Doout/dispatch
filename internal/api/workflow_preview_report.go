package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/go-chi/chi/v5"
)

type workflowPreviewReportRequest struct {
	GitHubAppID       string `json:"githubAppId"`
	Repository        string `json:"repository"`
	PullRequestNumber int    `json:"pullRequestNumber"`
	URL               string `json:"url"`
}

func (a *API) reportWorkflowPreview(w http.ResponseWriter, r *http.Request) {
	if a.eventConfig.GitHubApps == nil {
		problem(w, http.StatusServiceUnavailable, "GitHub App unavailable", "Configure a GitHub App before reporting a preview.")
		return
	}
	var input workflowPreviewReportRequest
	if !decode(w, r, &input) {
		return
	}
	input.GitHubAppID = strings.TrimSpace(input.GitHubAppID)
	input.Repository = events.NormalizeRepository(input.Repository)
	input.URL = strings.TrimSpace(input.URL)
	parsedURL, err := url.Parse(input.URL)
	if input.GitHubAppID == "" || input.Repository == "" || input.PullRequestNumber < 1 ||
		err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.Fragment != "" {
		problem(w, http.StatusBadRequest, "Invalid preview report", "Provide a GitHub App, linked pull request, and HTTPS preview URL.")
		return
	}
	a.previewReportMu.Lock()
	defer a.previewReportMu.Unlock()
	revision, err := a.store.GetWorkflowRevision(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow revision")
		return
	}
	if revision.State != "succeeded" {
		problem(w, http.StatusConflict, "Preview is not ready", "Report a preview after its workflow revision succeeds.")
		return
	}
	linked := false
	for _, source := range revision.Sources {
		if events.NormalizeRepository(source.Repository) == input.Repository {
			linked = true
			break
		}
	}
	if !linked {
		problem(w, http.StatusBadRequest, "Pull request is not linked", "The pull request repository must be one of the revision sources.")
		return
	}
	resource, err := a.store.GetWorkflowResource(r.Context(), revision.ResourceID)
	if err != nil {
		a.notFoundOrInternal(w, err, "Workflow resource")
		return
	}
	stages, err := a.store.ListWorkflowStageRuns(r.Context(), revision.ID)
	if err != nil {
		a.internal(w, err)
		return
	}
	connection, err := a.store.GetGitHubApp(r.Context(), input.GitHubAppID)
	if err != nil {
		a.notFoundOrInternal(w, err, "GitHub App")
		return
	}
	if connection.InstallationID < 1 {
		problem(w, http.StatusConflict, "GitHub App not installed", "Install the selected GitHub App for the pull request repository.")
		return
	}
	resolver := events.GitHubResolver{BaseURL: connection.APIURL, RepositoryTokenSource: func(ctx context.Context, repository string) (string, error) {
		return a.eventConfig.GitHubApps.RepositoryToken(ctx, input.GitHubAppID, repository)
	}}
	pr, err := resolver.ResolvePullRequest(r.Context(), input.Repository, input.PullRequestNumber)
	if err != nil {
		problem(w, http.StatusBadGateway, "Pull request lookup failed", err.Error())
		return
	}
	if !pr.Open {
		problem(w, http.StatusConflict, "Pull request closed", "A preview report needs an open pull request.")
		return
	}
	commentID, err := a.store.WorkflowPreviewReportComment(r.Context(), revision.ID, input.Repository, input.PullRequestNumber)
	if err != nil {
		a.internal(w, err)
		return
	}
	notifier := events.GitHubNotifier{BaseURL: connection.APIURL, RepositoryTokenSource: resolver.RepositoryTokenSource}
	var reportTrigger *core.WorkflowPreviewTrigger
	triggers, err := a.store.ListWorkflowPreviewTriggers(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	for _, trigger := range triggers {
		if trigger.ResourceID == resource.ID && trigger.GitHubAppID == input.GitHubAppID && events.NormalizeRepository(trigger.Repository) == input.Repository && trigger.PullRequestNumber == input.PullRequestNumber && trigger.ClosedAt == nil {
			reportTrigger = &trigger
			break
		}
	}
	body := workflowPreviewReportBody(revision, resource, stages, input.URL, connection.WebURL)
	if reportTrigger != nil {
		body = workflowPreviewReportForTrigger(revision, resource, stages, input.URL, connection.WebURL, *reportTrigger)
	}
	commentID, err = postWorkflowPreviewComment(r.Context(), notifier, a.eventConfig.GitHubToken,
		input.Repository, input.PullRequestNumber, commentID, body)
	if err != nil {
		problem(w, http.StatusBadGateway, "Preview report failed", err.Error())
		return
	}
	if err := a.store.SaveWorkflowPreviewReportComment(r.Context(), revision.ID, input.Repository, input.PullRequestNumber, commentID); err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"commentId": commentID, "revisionId": revision.ID, "url": input.URL})
}

func postWorkflowPreviewComment(ctx context.Context, notifier events.GitHubNotifier, fallbackToken, repository string, number int, commentID, body string) (string, error) {
	updatedID, err := notifier.UpdateComment(ctx, repository, number, commentID, body)
	if errors.Is(err, events.ErrCommentForbidden) && fallbackToken != "" {
		// Keep the existing comment ID when switching credentials. A failed
		// UpdateComment returns an empty ID, which would create a duplicate.
		fallback := events.GitHubNotifier{BaseURL: notifier.BaseURL, Token: fallbackToken}
		return fallback.UpdateComment(ctx, repository, number, commentID, body)
	}
	return updatedID, err
}

// reportPendingWorkflowPreviews reconciles completed runs so a controller restart
// or a temporary GitHub error cannot silently lose the preview report.
func (a *API) reportPendingWorkflowPreviews(ctx context.Context, trigger core.WorkflowPreviewTrigger) error {
	if a.eventConfig.GitHubApps == nil || trigger.PreviewURL == "" {
		return nil
	}
	a.previewReportMu.Lock()
	defer a.previewReportMu.Unlock()
	pending, err := a.store.PendingWorkflowPreviewReports(ctx, trigger.ID)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	connection, err := a.store.GetGitHubApp(ctx, trigger.GitHubAppID)
	if err != nil {
		return err
	}
	resolver := events.GitHubResolver{BaseURL: connection.APIURL, RepositoryTokenSource: func(ctx context.Context, repository string) (string, error) {
		return a.eventConfig.GitHubApps.RepositoryToken(ctx, trigger.GitHubAppID, repository)
	}}
	pr, err := resolver.ResolvePullRequest(ctx, trigger.Repository, trigger.PullRequestNumber)
	if err != nil {
		return err
	}
	if !pr.Open {
		return nil
	}
	notifier := events.GitHubNotifier{BaseURL: connection.APIURL, RepositoryTokenSource: resolver.RepositoryTokenSource}
	resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
	if err != nil {
		return err
	}
	for _, revisionID := range pending {
		revision, err := a.store.GetWorkflowRevision(ctx, revisionID)
		if err != nil {
			return err
		}
		stages, err := a.store.ListWorkflowStageRuns(ctx, revision.ID)
		if err != nil {
			return err
		}
		body := workflowPreviewReportForTrigger(revision, resource, stages, trigger.PreviewURL, connection.WebURL, trigger)
		commentID, err := notifier.UpdateComment(ctx, trigger.Repository, trigger.PullRequestNumber, trigger.ReportCommentID, body)
		if err != nil {
			return err
		}
		if err := a.store.UpdateWorkflowPreviewTriggerComment(ctx, trigger.ID, commentID); err != nil {
			return err
		}
		trigger.ReportCommentID = commentID
		if err := a.store.SaveWorkflowPreviewReportComment(ctx, revision.ID, trigger.Repository, trigger.PullRequestNumber, commentID); err != nil {
			return err
		}
	}
	return nil
}

func workflowPreviewReportBody(revision core.WorkflowRevision, resource core.WorkflowResource, stages []core.WorkflowStageRun, previewURL, githubURL string, links ...core.HelmPullRequest) string {
	var body strings.Builder
	fmt.Fprintf(&body, "<!-- dispatch-workflow-preview:%s -->\n### %s preview\n\n", revision.ID, resource.Name)
	fmt.Fprintf(&body, "**Status:** Ready\n**URL:** [Open preview](%s)\n", previewURL)
	for _, stage := range stages {
		if stage.TargetRef != "" {
			fmt.Fprintf(&body, "**Target:** `%s`\n", stage.TargetRef)
		}
		for _, result := range stage.DeploymentResults {
			fmt.Fprintf(&body, "**Deployment:** `%s` (%s)\n", result.DeploymentName, result.Outcome)
		}
	}
	if len(links) > 0 {
		sort.Slice(links, func(i, j int) bool { return links[i].Repository < links[j].Repository })
		body.WriteString("\n**Linked pull requests**\n\n")
		for _, link := range links {
			fmt.Fprintf(&body, "- [%s #%d](%s/%s/pull/%d)\n", link.Repository, link.Number, strings.TrimRight(githubURL, "/"), link.Repository, link.Number)
		}
	}
	aliases := make([]string, 0, len(revision.Sources))
	for alias := range revision.Sources {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	body.WriteString("\n| Source | Repository | Ref | Commit |\n| --- | --- | --- | --- |\n")
	for _, alias := range aliases {
		source := revision.Sources[alias]
		commitURL := strings.TrimRight(githubURL, "/") + "/" + source.Repository + "/commit/" + source.CommitSHA
		fmt.Fprintf(&body, "| %s | %s | `%s` | [`%s`](%s) |\n", alias, source.Repository, source.Branch, source.CommitSHA, commitURL)
	}
	jobs := make([]string, 0, len(revision.Outputs))
	for name, output := range revision.Outputs {
		if output["image"] != "" || output["imageRepository"] != "" && output["imageTag"] != "" {
			jobs = append(jobs, name)
		}
	}
	if len(jobs) > 0 {
		sort.Strings(jobs)
		body.WriteString("\n**Built images**\n\n")
		for _, name := range jobs {
			output := revision.Outputs[name]
			image := output["image"]
			if image == "" {
				image = output["imageRepository"] + ":" + output["imageTag"]
			}
			fmt.Fprintf(&body, "- `%s`: `%s`\n", name, image)
		}
	}
	return body.String()
}

func workflowPreviewCommandHelp(revision core.WorkflowRevision, trigger core.WorkflowPreviewTrigger) string {
	command := trigger.Command
	if command == "" {
		command = "/preview"
	}
	aliases := make([]string, 0, len(revision.Sources))
	for alias, source := range revision.Sources {
		if events.NormalizeRepository(source.Repository) != events.NormalizeRepository(trigger.Repository) {
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	var body strings.Builder
	body.WriteString("\n### Preview commands\n\nPost a new comment on this PR:\n\n")
	fmt.Fprintf(&body, "- `%s`: run again with the latest commits from this PR and its linked PRs.\n", command)
	if len(aliases) == 0 {
		return body.String()
	}
	example := aliases[0]
	for _, alias := range aliases {
		if alias == "ui" || alias == "service" {
			example = alias
			break
		}
	}
	fmt.Fprintf(&body, "- `%s with %s=#<PR_NUMBER>`: link or replace a PR from `%s`.\n", command, example, revision.Sources[example].Repository)
	body.WriteString("\nReplace `<PR_NUMBER>` with the PR number from that source's repository. Saved links remain attached to later runs; repeat `with` for an alias to replace its link.\n")
	body.WriteString("\n<details>\n<summary>All source overrides</summary>\n\n| Source | Repository | Command |\n| --- | --- | --- |\n")
	for _, alias := range aliases {
		fmt.Fprintf(&body, "| `%s` | `%s` | `%s with %s=#<PR_NUMBER>` |\n", alias, revision.Sources[alias].Repository, command, alias)
	}
	if len(aliases) > 1 {
		other := aliases[0]
		if other == example {
			other = aliases[1]
		}
		fmt.Fprintf(&body, "\nCombine overrides with commas: `%s with %s=#<PR_NUMBER>,%s=#<PR_NUMBER>`.\n", command, example, other)
	}
	body.WriteString("\nThe source for this PR is selected automatically. Linked source overrides do not change which repositories watch for commands.\n\n</details>\n")
	return body.String()
}

func workflowPreviewReportForTrigger(revision core.WorkflowRevision, resource core.WorkflowResource, stages []core.WorkflowStageRun, previewURL, githubURL string, trigger core.WorkflowPreviewTrigger) string {
	links := []core.HelmPullRequest{}
	for alias, number := range trigger.LinkedPullRequests {
		if source, ok := revision.Sources[alias]; ok {
			links = append(links, core.HelmPullRequest{Repository: source.Repository, Number: number})
		}
	}
	body := workflowPreviewReportBody(revision, resource, stages, previewURL, githubURL, links...)
	body += workflowPreviewCommandHelp(revision, trigger)
	if source := trigger.TemplateSource; source != nil && source.CommitSHA != "" {
		body += fmt.Sprintf("\n**Template:** `%s/%s` at [`%s`](%s/%s/commit/%s)\n", source.Repository, source.Path, source.CommitSHA, strings.TrimRight(githubURL, "/"), source.Repository, source.CommitSHA)
	}
	return body
}

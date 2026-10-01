package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/oklog/ulid/v2"
)

func (a *API) previewPanelGitHub(ctx context.Context, connectionID, repository string) (events.GitHubCommentReader, events.GitHubResolver, events.GitHubNotifier, string, error) {
	connection, err := a.store.GetGitHubApp(ctx, connectionID)
	if err != nil {
		return events.GitHubCommentReader{}, events.GitHubResolver{}, events.GitHubNotifier{}, "", err
	}
	if a.eventConfig.GitHubApps == nil {
		return events.GitHubCommentReader{}, events.GitHubResolver{}, events.GitHubNotifier{}, "", errors.New("GitHub App unavailable")
	}
	tokenSource := func(ctx context.Context) (string, error) {
		return a.eventConfig.GitHubApps.RepositoryToken(ctx, connectionID, repository)
	}
	repositoryTokenSource := func(ctx context.Context, repo string) (string, error) {
		return a.eventConfig.GitHubApps.RepositoryToken(ctx, connectionID, repo)
	}
	return events.GitHubCommentReader{BaseURL: connection.APIURL, TokenSource: tokenSource}, events.GitHubResolver{BaseURL: connection.APIURL, RepositoryTokenSource: repositoryTokenSource}, events.GitHubNotifier{BaseURL: connection.APIURL, RepositoryTokenSource: repositoryTokenSource}, connection.WebURL, nil
}

func previewTriggerLinks(trigger core.WorkflowPreviewTrigger, resource core.WorkflowResource) map[string]int {
	links := map[string]int{events.NormalizeRepository(trigger.Repository): trigger.PullRequestNumber}
	documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return links
	}
	for alias, number := range trigger.LinkedPullRequests {
		if source, ok := documents[0].Spec.Sources[alias]; ok {
			links[events.NormalizeRepository(source.Repository)] = number
		}
	}
	return links
}

// Commands on a linked PR operate on the existing preview and resolve the
// canonical PR's head. They must not create a second independent preview.
func (a *API) canonicalWorkflowPreviewTarget(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, resolver events.GitHubResolver) (*previewPollTarget, core.IncomingEvent, error) {
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, event, err
	}
	for _, trigger := range triggers {
		if trigger.ClosedAt != nil || trigger.GitHubAppID != target.connectionID || trigger.Command != event.Command {
			continue
		}
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			return nil, event, err
		}
		if previewTriggerLinks(trigger, resource)[target.repository] != event.PullRequestNumber {
			continue
		}
		if events.NormalizeRepository(trigger.Repository) == target.repository {
			return target, event, nil
		}
		primary, err := resolver.ResolvePullRequest(ctx, trigger.Repository, trigger.PullRequestNumber)
		if err != nil {
			return nil, event, err
		}
		copied := *target
		copied.repository = events.NormalizeRepository(trigger.Repository)
		copied.workflowTriggers = []core.WorkflowPreviewTrigger{trigger}
		copied.workflowTemplates = nil
		event.Repository = copied.repository
		event.PullRequestNumber = trigger.PullRequestNumber
		event.HeadSHA, event.HeadRef, event.BaseRef = primary.HeadSHA, primary.HeadRef, primary.BaseRef
		return &copied, event, nil
	}
	return target, event, nil
}

func (a *API) discoverWorkflowPreviewPanels(ctx context.Context, target *previewPollTarget, reader events.GitHubCommentReader) error {
	if !slices.ContainsFunc(target.workflowTemplates, func(t core.WorkflowPreviewTemplate) bool { return t.CommentOnOpen }) {
		return nil
	}
	prs, err := reader.ListOpenPullRequests(ctx, target.repository)
	if err != nil {
		return err
	}
	for _, template := range target.workflowTemplates {
		if !template.CommentOnOpen {
			continue
		}
		for _, pr := range prs {
			if pr.CreatedAt.Before(template.CreatedAt) {
				continue
			}
			if err := a.ensureAvailableWorkflowPreviewPanel(ctx, template, target.repository, pr.Number); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *API) ensureAvailableWorkflowPreviewPanel(ctx context.Context, template core.WorkflowPreviewTemplate, repository string, number int) error {
	a.previewPanelMu.Lock()
	defer a.previewPanelMu.Unlock()
	release, err := a.claimWorkflowPreviewPanelLease(ctx)
	if err != nil {
		return err
	}
	defer release()

	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		return err
	}
	for _, p := range panels {
		if p.GitHubAppID == template.GitHubAppID && p.Repository == repository && p.PullRequestNumber == number && p.TemplateID == template.ID {
			return nil
		}
	}
	panel := core.WorkflowPreviewPanel{ID: ulid.Make().String(), TemplateID: template.ID, GitHubAppID: template.GitHubAppID, Repository: repository, PullRequestNumber: number}
	return a.writeWorkflowPreviewPanel(ctx, panel, template, nil, core.WorkflowResource{}, core.WorkflowRevision{}, nil, true)
}

func (a *API) writeWorkflowPreviewPanel(ctx context.Context, panel core.WorkflowPreviewPanel, template core.WorkflowPreviewTemplate, trigger *core.WorkflowPreviewTrigger, resource core.WorkflowResource, revision core.WorkflowRevision, stages []core.WorkflowStageRun, linked bool) error {
	reader, _, notifier, webURL, err := a.previewPanelGitHub(ctx, panel.GitHubAppID, panel.Repository)
	if err != nil {
		return err
	}
	if len(revision.Sources) == 0 {
		_, sources, err := workflow.ReadWorkflowTemplateTrigger([]byte(template.Document))
		if err == nil {
			revision.Sources = map[string]core.WorkflowSourceRevision{}
			for alias, source := range sources {
				revision.Sources[alias] = core.WorkflowSourceRevision{Repository: source.Repository, Branch: source.Branch, CommitSHA: source.Ref}
			}
		}
	}
	runs := previewPanelRuns{Latest: revision, Stages: stages}
	if trigger != nil && linked && !previewRemoved(resource) {
		revisions, err := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
		if err != nil {
			return err
		}
		for _, r := range revisions {
			if strings.HasPrefix(r.Trigger, "pull request test ") {
				if runs.Checks.ID == "" {
					runs.Checks = r
				}
				continue
			}
			if r.State == "succeeded" && runs.Deployed.ID == "" {
				runs.Deployed = r
				runs.DeployedStages, err = a.store.ListWorkflowStageRuns(ctx, r.ID)
				if err != nil {
					return err
				}
			}
		}
	}
	body := previewPanelBody(panel, template, trigger, resource, runs, webURL, linked)
	if body == panel.Body && panel.CommentID != "" {
		return nil
	}
	if panel.CommentID != "" && panel.Body != "" && !previewRemoved(resource) {
		current, err := reader.GetComment(ctx, panel.Repository, panel.CommentID)
		if err != nil {
			return err
		}
		if len(previewPanelActions(panel.Body, current.Body)) > 0 {
			return nil
		} // Polling will consume the edit first.
	}
	id, err := notifier.UpdateComment(ctx, panel.Repository, panel.PullRequestNumber, panel.CommentID, body)
	if err != nil {
		return err
	}
	panel.CommentID, panel.Body = id, body
	if err := a.store.SaveWorkflowPreviewPanel(ctx, panel); err != nil {
		return err
	}
	if trigger != nil && trigger.ClosedAt == nil && panel.Repository == events.NormalizeRepository(trigger.Repository) && panel.PullRequestNumber == trigger.PullRequestNumber {
		return a.store.UpdateWorkflowPreviewTriggerComment(ctx, trigger.ID, id)
	}
	return nil
}

// Reconcile every panel, including mirrored comments and removed previews.
// GitHub failures remain retryable after a controller restart.
func (a *API) syncWorkflowPreviewPanels(ctx context.Context) error {
	if a.eventConfig.GitHubApps == nil {
		return nil
	}
	a.previewPanelMu.Lock()
	defer a.previewPanelMu.Unlock()
	release, err := a.claimWorkflowPreviewPanelLease(ctx)
	if err != nil {
		return err
	}
	defer release()

	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		return err
	}
	var joined error
	for _, trigger := range triggers {
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		revisions, err := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		revision := core.WorkflowRevision{}
		var stages []core.WorkflowStageRun
		for _, r := range revisions {
			if !strings.HasPrefix(r.Trigger, "pull request test ") {
				revision = r
				break
			}
		}
		if revision.ID != "" {
			stages, err = a.store.ListWorkflowStageRuns(ctx, revision.ID)
			if err != nil {
				joined = errors.Join(joined, err)
				continue
			}
		}
		if !previewRemoved(resource) && trigger.ClosedAt == nil && strings.HasPrefix(revision.Trigger, "pull request comment ") && (revision.State == "queued" || revision.State == "running" || revision.State == "awaiting_approval") {
			if err := a.postWorkflowPreviewRunStarted(ctx, trigger, revision, strings.TrimPrefix(revision.Trigger, "pull request comment ")); err != nil {
				joined = errors.Join(joined, err)
			}
		}
		template := core.WorkflowPreviewTemplate{}
		if trigger.TemplateID != "" {
			template, err = a.store.GetWorkflowPreviewTemplate(ctx, trigger.TemplateID)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				joined = errors.Join(joined, err)
				continue
			}
		}
		links := previewTriggerLinks(trigger, resource)
		desired := []core.WorkflowPreviewPanel{}
		for repository, number := range links {
			var panel core.WorkflowPreviewPanel
			for _, p := range panels {
				if p.GitHubAppID == trigger.GitHubAppID && p.Repository == repository && p.PullRequestNumber == number && p.TemplateID == trigger.TemplateID {
					panel = p
					break
				}
			}
			if panel.ID == "" {
				adoptingClosedReport := trigger.ClosedAt != nil && repository == events.NormalizeRepository(trigger.Repository) && trigger.ReportCommentID != ""
				if trigger.ClosedAt != nil && !adoptingClosedReport {
					continue
				}
				panel = core.WorkflowPreviewPanel{ID: ulid.Make().String(), TemplateID: trigger.TemplateID, GitHubAppID: trigger.GitHubAppID, Repository: repository, PullRequestNumber: number}
				if repository == events.NormalizeRepository(trigger.Repository) {
					panel.CommentID = trigger.ReportCommentID
				}
				if adoptingClosedReport {
					panel.ResourceID = resource.ID
				}
			}
			if panel.ResourceID != "" && panel.ResourceID != resource.ID {
				other, err := a.store.GetWorkflowResource(ctx, panel.ResourceID)
				if err == nil && !previewRemoved(other) {
					joined = errors.Join(joined, fmt.Errorf("PR %s #%d already belongs to another preview", repository, number))
					continue
				}
				if trigger.ClosedAt != nil {
					continue
				}
			}
			if trigger.ClosedAt != nil && panel.ResourceID != resource.ID {
				continue
			}
			panel.ResourceID = resource.ID
			desired = append(desired, panel)
		}
		for _, p := range panels {
			if p.ResourceID == resource.ID && links[p.Repository] != p.PullRequestNumber {
				desired = append(desired, p)
			}
		}
		for _, panel := range desired {
			if err := a.writeWorkflowPreviewPanel(ctx, panel, template, &trigger, resource, revision, stages, links[panel.Repository] == panel.PullRequestNumber); err != nil {
				joined = errors.Join(joined, err)
			}
		}
	}
	panels, err = a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		return errors.Join(joined, err)
	}
	for _, panel := range panels {
		if panel.ResourceID != "" {
			continue
		}
		template, err := a.store.GetWorkflowPreviewTemplate(ctx, panel.TemplateID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			joined = errors.Join(joined, err)
			continue
		}
		joined = errors.Join(joined, a.writeWorkflowPreviewPanel(ctx, panel, template, nil, core.WorkflowResource{}, core.WorkflowRevision{}, nil, true))
	}
	return joined
}

func (a *API) processWorkflowPreviewPanelEdit(ctx context.Context, target *previewPollTarget, reader events.GitHubCommentReader, comment events.GitHubComment) (bool, error) {
	if !strings.Contains(comment.Body, "<!-- dispatch-preview-panel:") {
		return false, nil
	}
	a.previewPanelMu.Lock()
	defer a.previewPanelMu.Unlock()
	release, err := a.claimWorkflowPreviewPanelLease(ctx)
	if err != nil {
		return true, err
	}
	defer release()

	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		return false, err
	}
	for _, panel := range panels {
		if panel.GitHubAppID != target.connectionID || panel.Repository != target.repository || panel.CommentID != comment.ID.String() {
			continue
		}
		actions := previewPanelActions(panel.Body, comment.Body)
		if len(actions) == 0 {
			return true, nil
		}
		editor, err := reader.AuthorizedCommentEditor(ctx, target.repository, comment)
		if err != nil {
			return true, err
		}
		if editor == "" {
			panel.Body = comment.Body
			return true, a.store.SaveWorkflowPreviewPanel(ctx, panel)
		}
		_, resolver, _, _, err := a.previewPanelGitHub(ctx, target.connectionID, target.repository)
		if err != nil {
			return true, err
		}
		pr, err := resolver.ResolvePullRequest(ctx, target.repository, panel.PullRequestNumber)
		if err != nil {
			return true, err
		}
		if !pr.Open {
			return true, nil
		}
		template, err := a.store.GetWorkflowPreviewTemplate(ctx, panel.TemplateID)
		if err != nil && panel.ResourceID == "" {
			return true, err
		}
		command := template.Command
		if panel.ResourceID != "" {
			triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
			if err != nil {
				return true, err
			}
			valid := false
			for _, trigger := range triggers {
				if trigger.ResourceID != panel.ResourceID || trigger.ClosedAt != nil {
					continue
				}
				resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
				if err != nil {
					return true, err
				}
				if previewTriggerLinks(trigger, resource)[target.repository] != panel.PullRequestNumber {
					continue
				}
				command = trigger.Command
				valid = true
				break
			}
			if !valid {
				return true, nil
			}
		} else if !template.Active {
			return true, nil
		}
		hash := sha256.Sum256([]byte(comment.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00") + comment.Body))
		for index, action := range actions {
			event := core.IncomingEvent{ControlAction: true, Kind: core.EventKindPullRequestComment, Action: "edited", ProviderConnectionID: target.connectionID, Repository: target.repository, PullRequestNumber: panel.PullRequestNumber, Command: command, Arguments: action, SourceCommentID: fmt.Sprintf("panel:%s:%x:%d", panel.CommentID, hash, index), DeliveryID: fmt.Sprintf("panel:%s:%x:%d", panel.CommentID, hash, index), TrustedActor: true, Actor: editor, HeadSHA: pr.HeadSHA, HeadRef: pr.HeadRef, BaseRef: pr.BaseRef}
			if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
				panel.ActionError = "The requested action could not start. Open Events in Dispatch for the error, then check the action to retry."
				panel.Body = comment.Body
				saveErr := a.store.SaveWorkflowPreviewPanel(ctx, panel)
				return true, errors.Join(err, saveErr)
			}
		}
		// Persist acceptance before resetting the checkbox. Run reservations protect
		// retries if processing succeeded but writing the panel fails.
		panel.Body = comment.Body
		panel.ActionError = ""
		if err := a.store.SaveWorkflowPreviewPanel(ctx, panel); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

func (a *API) postWorkflowPreviewRunStarted(ctx context.Context, trigger core.WorkflowPreviewTrigger, revision core.WorkflowRevision, sourceCommentID string) error {
	if a.eventConfig.GitHubApps == nil {
		return nil
	}
	existing, err := a.store.WorkflowPreviewTestComment(ctx, revision.ID)
	if err != nil || existing != "" {
		return err
	}
	_, _, notifier, _, err := a.previewPanelGitHub(ctx, trigger.GitHubAppID, trigger.Repository)
	if err != nil {
		return err
	}
	body := fmt.Sprintf("<!-- dispatch-preview-run:%s -->\nPreview deployment %s. The preview panel will update with the result.\n", revision.ID, revision.State)
	id, err := notifier.UpdateComment(ctx, trigger.Repository, trigger.PullRequestNumber, "", body)
	if err != nil {
		return err
	}
	return a.store.UpdateWorkflowPreviewTestComment(ctx, trigger.ID, sourceCommentID, id)
}

func (a *API) claimWorkflowPreviewPanelLease(ctx context.Context) (func(), error) {
	holder := ulid.Make().String()
	claimed, err := a.store.ClaimWorkflowPreviewPanelLease(ctx, holder, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errors.New("preview panels are being updated by another controller; retrying")
	}
	return func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = a.store.ReleaseWorkflowPreviewPanelLease(cleanup, holder)
	}, nil
}

func (a *API) workflowPreviewOpenAfterClosure(ctx context.Context, trigger core.WorkflowPreviewTrigger, closedRepository string, resolver events.GitHubResolver) (bool, error) {
	if events.NormalizeRepository(trigger.Repository) != closedRepository {
		primary, err := resolver.ResolvePullRequest(ctx, trigger.Repository, trigger.PullRequestNumber)
		if err != nil {
			return false, err
		}
		if primary.Open {
			return true, nil
		}
	}
	return a.workflowPreviewLinkedPullRequestOpen(ctx, trigger, resolver)
}

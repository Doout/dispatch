package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/groups"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflow"
	"github.com/oklog/ulid/v2"
)

type previewPollTarget struct {
	connectionID      string
	repository        string
	commands          map[string]bool
	activePRs         map[int]bool
	workflowTriggers  []core.WorkflowPreviewTrigger
	workflowTemplates []core.WorkflowPreviewTemplate
}

func (a *API) RunPreviewPoller(ctx context.Context) {
	if a.eventConfig.GitHubApps == nil && a.eventConfig.GitHubToken == "" {
		return
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if err := a.PollPreviewsOnce(ctx); err != nil && a.logger != nil {
			a.logger.Error("preview poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// PollPreviewsOnce feeds polled comments and closures through the same event
// services used by signed webhooks. The cursor advances only after a full scan.
func (a *API) PollPreviewsOnce(ctx context.Context) error {
	joined := a.syncWorkflowPreviewTemplates(ctx)
	targets, err := a.previewPollTargets(ctx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := a.pollPreviewTarget(ctx, target); err != nil {
			joined = errors.Join(joined, fmt.Errorf("%s: %w", target.repository, err))
		}
	}
	return joined
}

func (a *API) previewPollTargets(ctx context.Context) ([]*previewPollTarget, error) {
	groupsList, err := a.store.ListPreviewGroups(ctx)
	if err != nil {
		return nil, err
	}
	triggers, err := a.store.ListEventTriggers(ctx, "")
	if err != nil {
		return nil, err
	}
	groupRuns, err := a.store.ListPreviewGroupRuns(ctx, "")
	if err != nil {
		return nil, err
	}
	previews, err := a.store.ListPreviewEnvironments(ctx, "")
	if err != nil {
		return nil, err
	}
	workflowTriggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, err
	}
	workflowTemplates, err := a.store.ListWorkflowPreviewTemplates(ctx)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*previewPollTarget{}
	get := func(connectionID, repository string) *previewPollTarget {
		if connectionID == "" && a.eventConfig.GitHubToken == "" || connectionID != "" && a.eventConfig.GitHubApps == nil {
			return nil
		}
		repository = events.NormalizeRepository(repository)
		key := connectionID + ":" + repository
		if target := byKey[key]; target != nil {
			return target
		}
		target := &previewPollTarget{connectionID: connectionID, repository: repository, commands: map[string]bool{}, activePRs: map[int]bool{}}
		byKey[key] = target
		return target
	}
	groupByID := map[string]core.PreviewGroup{}
	for _, group := range groupsList {
		groupByID[group.ID] = group
		for _, component := range group.Components {
			if target := get(group.GitHubAppID, component.Repository); target != nil && group.Enabled {
				target.commands[group.Command] = true
			}
		}
	}
	triggerByID := map[string]core.EventTrigger{}
	for _, trigger := range triggers {
		triggerByID[trigger.ID] = trigger
		if trigger.Provider == core.EventProviderGitHub && trigger.Enabled {
			if target := get(trigger.GitHubAppID, trigger.Repository); target != nil {
				target.commands[trigger.Command] = true
			}
		}
	}
	for _, run := range groupRuns {
		if !run.State.Active() {
			continue
		}
		group, ok := groupByID[run.GroupID]
		if !ok {
			continue
		}
		for _, source := range run.Sources {
			if source.PullRequest > 0 && source.ClosedAt == nil {
				if target := get(group.GitHubAppID, source.Repository); target != nil {
					target.activePRs[source.PullRequest] = true
				}
			}
		}
	}
	for _, preview := range previews {
		if preview.State == core.PreviewClosed || preview.ClosedAt != nil {
			continue
		}
		trigger, ok := triggerByID[preview.TriggerID]
		if ok {
			if target := get(trigger.GitHubAppID, preview.Repository); target != nil {
				target.activePRs[preview.PullRequestNumber] = true
			}
		}
	}
	for _, trigger := range workflowTriggers {
		if trigger.ClosedAt != nil {
			continue
		}
		if target := get(trigger.GitHubAppID, trigger.Repository); target != nil {
			target.commands[trigger.Command] = true
			target.activePRs[trigger.PullRequestNumber] = true
			target.workflowTriggers = append(target.workflowTriggers, trigger)
		}
	}
	for _, template := range workflowTemplates {
		if !template.Active {
			continue
		}
		for _, repository := range previewTemplateRepositories(template) {
			if target := get(template.GitHubAppID, repository); target != nil {
				target.commands[template.Command] = true
				target.workflowTemplates = append(target.workflowTemplates, template)
			}
		}
	}
	targets := make([]*previewPollTarget, 0, len(byKey))
	for _, target := range byKey {
		targets = append(targets, target)
	}
	return targets, nil
}

func (a *API) pollPreviewTarget(ctx context.Context, target *previewPollTarget) error {
	reader := events.GitHubCommentReader{BaseURL: a.eventConfig.GitHubAPIURL, Token: a.eventConfig.GitHubToken}
	resolver := events.GitHubResolver{BaseURL: reader.BaseURL, Token: reader.Token}
	groupService, eventService := a.groups, a.events
	if target.connectionID != "" {
		connection, err := a.store.GetGitHubApp(ctx, target.connectionID)
		if err != nil {
			return err
		}
		services, err := a.githubAppServices(ctx, target.connectionID)
		if err != nil {
			return err
		}
		tokenSource := func(ctx context.Context) (string, error) {
			return a.eventConfig.GitHubApps.InstallationToken(ctx, target.connectionID)
		}
		reader.BaseURL, reader.Token, reader.TokenSource = connection.APIURL, "", tokenSource
		resolver.BaseURL, resolver.Token, resolver.TokenSource = connection.APIURL, "", tokenSource
		groupService, eventService = services.groups, services.events
	}
	checkedAt := time.Now().UTC()
	cursor, err := a.store.PreviewPollCursor(ctx, target.connectionID, target.repository)
	if err != nil {
		return err
	}
	since := checkedAt.Add(-2 * time.Minute)
	if cursor != nil {
		since = cursor.Add(-time.Minute)
	}
	if len(target.commands) > 0 {
		comments, err := reader.ListIssueComments(ctx, target.repository, since)
		if err != nil {
			return err
		}
		for _, comment := range comments {
			// GitHub's since filter uses updated_at. An older comment edited
			// recently can still be a new command for a newly bound workflow.
			if comment.CreatedAt.After(checkedAt) {
				continue
			}
			command, _ := events.ParseCommand(comment.Body)
			if !target.commands[command] {
				continue
			}
			number, err := comment.IssueNumber(target.repository)
			if err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]any{
				"action": "created", "repository": map[string]any{"full_name": target.repository},
				"sender":  map[string]any{"login": comment.User.Login},
				"issue":   map[string]any{"number": number, "pull_request": map[string]any{}},
				"comment": comment,
			})
			if err != nil {
				return err
			}
			event, err := events.ParseGitHubEvent("issue_comment", "poll:comment:"+comment.ID.String(), payload, comment.CreatedAt)
			if err != nil {
				return err
			}
			event.ProviderConnectionID = target.connectionID
			event.DeliveryID = events.CommentDeliveryID(event)
			if !event.TrustedActor {
				continue
			}
			revision, err := resolver.ResolvePullRequest(ctx, target.repository, number)
			if errors.Is(err, events.ErrPullRequestNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			event.HeadRef, event.HeadSHA, event.BaseRef = revision.HeadRef, revision.HeadSHA, revision.BaseRef
			if !revision.Open {
				continue
			}
			if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
				return err
			}
			seen, err := a.store.IncomingEventExists(ctx, event.Provider, event.DeliveryID)
			if err != nil {
				return err
			}
			if seen {
				continue
			}
			if _, err := groupService.Process(ctx, event); err != nil {
				return err
			}
			if _, err := eventService.Process(ctx, event); err != nil {
				return err
			}
		}
	}
	if err := a.store.SavePreviewPollCursor(ctx, target.connectionID, target.repository, checkedAt); err != nil {
		return err
	}
	for number := range target.activePRs {
		revision, err := resolver.ResolvePullRequest(ctx, target.repository, number)
		if err != nil {
			return err
		}
		if revision.Open {
			continue
		}
		payload, err := json.Marshal(map[string]any{
			"action": "closed", "repository": map[string]any{"full_name": target.repository},
			"pull_request": map[string]any{"number": number, "head": map[string]any{"ref": revision.HeadRef, "sha": revision.HeadSHA}, "base": map[string]any{"ref": revision.BaseRef}},
		})
		if err != nil {
			return err
		}
		event, err := events.ParseGitHubEvent("pull_request", strings.Join([]string{"poll:closed", target.repository, strconv.Itoa(number)}, ":"), payload, time.Now().UTC())
		if err != nil {
			return err
		}
		event.ProviderConnectionID = target.connectionID
		if _, err := groupService.Process(ctx, event); err != nil {
			return err
		}
		if _, err := eventService.Process(ctx, event); err != nil {
			return err
		}
		for _, trigger := range target.workflowTriggers {
			if trigger.PullRequestNumber == number {
				linkedOpen, err := a.workflowPreviewLinkedPullRequestOpen(ctx, trigger, resolver)
				if err != nil {
					return err
				}
				if linkedOpen {
					continue
				}
				if err := a.closeWorkflowPreview(ctx, trigger); err != nil {
					return err
				}
			}
		}
	}
	for _, trigger := range target.workflowTriggers {
		if trigger.PreviewURL == "" {
			continue
		}
		if err := a.reportPendingWorkflowPreviews(ctx, trigger); err != nil {
			return fmt.Errorf("report preview %s: %w", trigger.ID, err)
		}
	}
	return nil
}

func (a *API) workflowPreviewLinkedPullRequestOpen(ctx context.Context, trigger core.WorkflowPreviewTrigger, resolver events.GitHubResolver) (bool, error) {
	if len(trigger.LinkedPullRequests) == 0 {
		return false, nil
	}
	resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
	if err != nil {
		return false, err
	}
	documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil || len(documents) != 1 || documents[0].Spec == nil {
		return false, errors.New("temporary preview document is invalid")
	}
	for alias, number := range trigger.LinkedPullRequests {
		source, ok := documents[0].Spec.Sources[alias]
		if !ok {
			return false, fmt.Errorf("linked component %q is missing", alias)
		}
		pr, err := resolver.ResolvePullRequest(ctx, source.Repository, number)
		if errors.Is(err, events.ErrPullRequestNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if pr.Open {
			return true, nil
		}
	}
	return false, nil
}

func (a *API) closeWorkflowPreview(ctx context.Context, trigger core.WorkflowPreviewTrigger) error {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
	if err != nil {
		return err
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return err
	}
	for _, other := range triggers {
		if other.ID == trigger.ID && other.ClosedAt != nil {
			return nil
		}
		if other.ResourceID == resource.ID && other.ID != trigger.ID && other.ClosedAt == nil {
			return a.store.CloseWorkflowPreviewTrigger(ctx, trigger.ID, time.Now().UTC())
		}
	}
	if err := a.cleanupWorkflowPreviewResource(ctx, resource); err != nil {
		return err
	}
	return a.store.CloseWorkflowPreviewTrigger(ctx, trigger.ID, time.Now().UTC())
}

func (a *API) cleanupWorkflowPreviewResource(ctx context.Context, resource core.WorkflowResource) error {
	appIDs := map[string]bool{}
	apps, err := a.store.ListActiveApps(ctx)
	if err != nil {
		return err
	}
	for _, app := range apps {
		if app.HelmProvenance.WorkflowResourceID == resource.ID {
			appIDs[app.ID] = true
		}
	}
	// Older previews may predate Helm provenance, so retain their stage links.
	revisions, err := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
	if err != nil {
		return err
	}
	for _, revision := range revisions {
		stages, err := a.store.ListWorkflowStageRuns(ctx, revision.ID)
		if err != nil {
			return err
		}
		for _, stage := range stages {
			for _, result := range stage.DeploymentResults {
				if result.AppID != "" {
					appIDs[result.AppID] = true
				}
			}
		}
	}
	for appID := range appIDs {
		app, err := a.store.GetApp(ctx, appID)
		if err != nil {
			return err
		}
		if app.State == "closed" {
			continue
		}
		if err := a.deploy.Cleanup(ctx, appID, nil); err != nil {
			return fmt.Errorf("clean preview app %s: %w", appID, err)
		}
		app.State = "closed"
		if err := a.store.UpdateApp(ctx, app); err != nil {
			return err
		}
	}
	if _, err := a.workflows.Deactivate(ctx, resource.ID); err != nil {
		return err
	}
	return nil
}

func (a *API) processWorkflowPreviewComment(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, resolver events.GitHubResolver) error {
	a.temporaryPreviewMu.Lock()
	defer a.temporaryPreviewMu.Unlock()
	for _, candidate := range target.workflowTemplates {
		template, err := a.store.GetWorkflowPreviewTemplate(ctx, candidate.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !template.Active || !slices.Contains(previewTemplateRepositories(template), target.repository) || template.GitHubAppID != target.connectionID {
			continue
		}
		if template.Command != event.Command {
			continue
		}
		triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
		if err != nil {
			return err
		}
		bound := false
		for _, trigger := range triggers {
			if trigger.ClosedAt == nil && trigger.GitHubAppID == template.GitHubAppID && events.NormalizeRepository(trigger.Repository) == target.repository && trigger.PullRequestNumber == event.PullRequestNumber && trigger.Command == event.Command {
				bound = true
				present := false
				for _, current := range target.workflowTriggers {
					if current.ID == trigger.ID {
						present = true
						break
					}
				}
				if !present {
					target.workflowTriggers = append(target.workflowTriggers, trigger)
				}
				break
			}
		}
		if bound {
			continue
		}
		if template.GitSource != nil && template.GitSource.LastError != "" {
			return fmt.Errorf("template %s is waiting for GitHub sync: %s", template.Name, template.GitSource.LastError)
		}
		existing, err := a.store.ListWorkflowResources(ctx, "")
		if err != nil {
			return err
		}
		connection, err := a.store.GetGitHubApp(ctx, template.GitHubAppID)
		if err != nil {
			return err
		}
		variables := workflow.TemplateVariables{PRNumber: event.PullRequestNumber, Repository: target.repository, PRURL: fmt.Sprintf("%s/%s/pull/%d", strings.TrimRight(connection.WebURL, "/"), target.repository, event.PullRequestNumber)}
		rendered, previewID, err := allocateTemporaryPreviewID(template.Document, strconv.Itoa(event.PullRequestNumber), existing, variables)
		if err != nil {
			return fmt.Errorf("allocate preview from template %s: %w", template.Name, err)
		}
		variables.ID = previewID
		previewURL, err := workflow.RenderTemplateText(template.PreviewURL, variables)
		if err != nil {
			return err
		}
		resource, err := a.workflows.CreateTemporaryApplication(ctx, template.ConfigSourceID, []byte(rendered))
		if err != nil {
			return fmt.Errorf("create preview from template %s: %w", template.Name, err)
		}
		trigger := core.WorkflowPreviewTrigger{ID: ulid.Make().String(), TemplateID: template.ID, TemplateSource: template.GitSource, ResourceID: resource.ID,
			GitHubAppID: template.GitHubAppID, Repository: target.repository, PullRequestNumber: event.PullRequestNumber,
			Command: template.Command, PreviewURL: previewURL, CreatedAt: time.Now().UTC()}
		if err := a.store.CreateWorkflowPreviewTrigger(ctx, trigger); err != nil {
			resource.Active, resource.State, resource.UpdatedAt = false, "removed", time.Now().UTC()
			_ = a.store.UpdateWorkflowResource(ctx, resource)
			return err
		}
		target.workflowTriggers = append(target.workflowTriggers, trigger)
	}
	for index, trigger := range target.workflowTriggers {
		if trigger.Command != event.Command || trigger.PullRequestNumber != event.PullRequestNumber {
			continue
		}
		resource, err := a.store.GetWorkflowResource(ctx, trigger.ResourceID)
		if err != nil {
			return err
		}
		if !resource.Active || !resource.Temporary {
			continue
		}
		documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
		if err != nil || len(documents) != 1 || documents[0].Spec == nil {
			return errors.New("temporary preview document is invalid")
		}
		updates, err := parseWorkflowPreviewLinks(event.Arguments, documents[0].Spec.Sources, target.repository, event.PullRequestNumber)
		if err != nil {
			return err
		}
		links := map[string]int{}
		for alias, number := range trigger.LinkedPullRequests {
			links[alias] = number
		}
		for alias, number := range updates {
			links[alias] = number
		}
		refs := map[string]string{}
		matched := false
		for alias, source := range documents[0].Spec.Sources {
			if events.NormalizeRepository(source.Repository) == target.repository {
				refs[alias] = event.HeadSHA
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("temporary preview %s does not contain %s", resource.Name, target.repository)
		}
		for alias, number := range links {
			source, ok := documents[0].Spec.Sources[alias]
			if !ok {
				return fmt.Errorf("linked component %q is missing", alias)
			}
			pr, err := resolver.ResolvePullRequest(ctx, source.Repository, number)
			if err != nil {
				return fmt.Errorf("resolve %s pull request #%d: %w", alias, number, err)
			}
			if !pr.Open {
				return fmt.Errorf("%s pull request #%d is closed", alias, number)
			}
			refs[alias] = pr.HeadSHA
		}
		pinned, err := pinWorkflowPreviewSources(resource, refs)
		if err != nil {
			return err
		}
		reserved, err := a.store.ReserveWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID)
		if err != nil {
			return err
		}
		if !reserved {
			continue
		}
		if pinned.Document != resource.Document {
			if err := a.store.UpdateWorkflowResource(ctx, pinned); err != nil {
				_ = a.store.ReleaseWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID)
				return err
			}
		}
		if err := a.store.UpdateWorkflowPreviewTriggerLinks(ctx, trigger.ID, links); err != nil {
			_ = a.store.ReleaseWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID)
			return err
		}
		target.workflowTriggers[index].LinkedPullRequests = links
		revision, err := a.workflows.Start(ctx, resource.ID, "pull request comment "+event.SourceCommentID)
		if err != nil {
			_ = a.store.ReleaseWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID)
			return err
		}
		if err := a.store.CompleteWorkflowPreviewComment(ctx, trigger.ID, event.SourceCommentID, revision.ID); err != nil {
			return err
		}
	}
	return nil
}

var _ groups.Notifier = events.GitHubNotifier{}

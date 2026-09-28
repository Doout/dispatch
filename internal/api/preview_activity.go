package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/groups"
	"github.com/oklog/ulid/v2"
)

func previewDeliveryKey(event core.IncomingEvent, ruleID string) string {
	deliveryID := event.DeliveryID
	if deliveryID == "" && event.SourceCommentID != "" {
		deliveryID = "comment:" + event.SourceCommentID
	}
	return "delivery:" + event.ProviderConnectionID + ":" + events.NormalizeRepository(event.Repository) + ":" + deliveryID + ":" + ruleID
}

func (a *API) previewDeliveryActivity(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, state string, deliveryErr error) error {
	if event.Kind == core.EventKindPullRequestComment && !event.TrustedActor {
		return nil
	}
	rules, err := a.configuredEventRules(ctx)
	if err != nil {
		return err
	}
	transport := target.transport
	if transport == "" {
		transport = "poll"
	}
	var joined error
	for _, rule := range rules {
		if rule.Kind == "configuration" || !rule.Enabled || rule.ConnectionID != target.connectionID || !slices.ContainsFunc(rule.Repositories, func(repo string) bool { return events.NormalizeRepository(repo) == target.repository }) {
			continue
		}
		if event.Kind == core.EventKindPullRequestComment && rule.Command != event.Command {
			continue
		}
		if rule.PullRequest > 0 && rule.PullRequest != event.PullRequestNumber {
			continue
		}
		projects := rule.ProjectIDs
		name := rule.Name
		if rule.Kind == "group" && len(projects) > 1 {
			projects = rule.RepositoryProjects[target.repository]
			name = "Preview group event"
		}
		for _, projectID := range projects {

			item := core.EventActivity{ID: ulid.Make().String(), ProjectID: projectID, RuleID: rule.ID, Name: name, Transport: transport, Kind: string(event.Kind), Repository: target.repository, PullRequest: event.PullRequestNumber, Command: event.Command, CommitSHA: event.HeadSHA, State: state, CreatedAt: time.Now().UTC()}
			if event.Action != "" {
				item.Kind += " " + event.Action
			}
			if fields := strings.Fields(event.Arguments); event.Kind == core.EventKindPullRequestComment && len(fields) > 0 && fields[0] == "test" {
				item.Kind = "preview_test"
			}
			if deliveryErr != nil {
				item.Message = previewCommandError(event, deliveryErr).Error()
			}
			joined = errors.Join(joined, a.store.SaveEventActivity(ctx, previewDeliveryKey(event, rule.ID), item))
		}
	}
	return joined
}

func (a *API) workflowPreviewRunActivity(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, trigger core.WorkflowPreviewTrigger, resource core.WorkflowResource, revisionID string) error {
	source, err := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return err
	}
	ruleID, name := "preview:"+trigger.ID, resource.Name
	if trigger.TemplateID != "" {
		template, err := a.store.GetWorkflowPreviewTemplate(ctx, trigger.TemplateID)
		if err != nil {
			return err
		}
		ruleID, name = "template:"+template.ID, template.Name
	}
	transport := target.transport
	if transport == "" {
		transport = "poll"
	}
	kind := "pull_request_comment"
	if fields := strings.Fields(event.Arguments); len(fields) > 0 && fields[0] == "test" {
		kind = "preview_test"
	}
	item := core.EventActivity{ID: ulid.Make().String(), ProjectID: source.ProjectID, RuleID: ruleID, Name: name, Transport: transport, Kind: kind, Repository: target.repository, PullRequest: event.PullRequestNumber, Command: event.Command, CommitSHA: event.HeadSHA, State: "running", ResourceID: resource.ID, RevisionIDs: []string{revisionID}, PreviewURL: trigger.PreviewURL, CreatedAt: time.Now().UTC()}
	return a.store.SaveEventActivity(ctx, previewDeliveryKey(event, ruleID), item)
}

func (a *API) pollPreviewTarget(ctx context.Context, target *previewPollTarget) (result error) {
	rules, err := a.configuredEventRules(ctx)
	if err != nil {
		return err
	}
	projects := map[string]bool{}
	for _, rule := range rules {
		if rule.Kind != "configuration" && rule.ConnectionID == target.connectionID && slices.ContainsFunc(rule.Repositories, func(repo string) bool { return events.NormalizeRepository(repo) == target.repository }) {
			for _, id := range rule.ProjectIDs {
				projects[id] = true
			}
		}
	}
	target.transport = "poll"
	defer func() {
		for projectID := range projects {
			key := previewCheckKey(target.connectionID, target.repository)
			item := core.EventActivity{ID: ulid.Make().String(), ProjectID: projectID, RuleID: key, Name: target.repository, Transport: "poll", Kind: "preview_check", Repository: target.repository, State: "processed", CreatedAt: time.Now().UTC(), Check: true}
			if result != nil {
				item.State = "failed"
				item.Message = result.Error()
			}
			result = errors.Join(result, a.store.SavePollCheck(ctx, key, item))
		}
	}()
	return a.scanPreviewTarget(ctx, target)
}

// Signed webhooks use the same workflow command handler and comment reservation
// as polling. Receiving both must never start the same comment twice.
func (a *API) processWorkflowPreviewWebhook(ctx context.Context, event core.IncomingEvent) error {
	targets, err := a.previewPollTargets(ctx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.connectionID != event.ProviderConnectionID || target.repository != events.NormalizeRepository(event.Repository) {
			continue
		}
		target.transport = "webhook"
		resolver := events.GitHubResolver{BaseURL: a.eventConfig.GitHubAPIURL, Token: a.eventConfig.GitHubToken}
		if target.connectionID != "" {
			connection, err := a.store.GetGitHubApp(ctx, target.connectionID)
			if err != nil {
				return err
			}
			resolver.BaseURL = connection.APIURL
			resolver.RepositoryTokenSource = func(ctx context.Context, repository string) (string, error) {
				return a.eventConfig.GitHubApps.RepositoryToken(ctx, target.connectionID, repository)
			}
		}
		if event.Kind == core.EventKindPullRequestComment {
			if !event.TrustedActor || !target.commands[event.Command] {
				continue
			}
			pr, err := resolver.ResolvePullRequest(ctx, target.repository, event.PullRequestNumber)
			if err != nil {
				return err
			}
			if !pr.Open {
				continue
			}
			event.HeadSHA, event.HeadRef, event.BaseRef = pr.HeadSHA, pr.HeadRef, pr.BaseRef
			return a.processWorkflowPreviewComment(ctx, target, event, resolver)
		}
		if event.Kind == core.EventKindPullRequest {
			for _, trigger := range target.workflowTriggers {
				if trigger.PullRequestNumber != event.PullRequestNumber {
					continue
				}
				if event.Action == "closed" {
					linkedOpen, err := a.workflowPreviewLinkedPullRequestOpen(ctx, trigger, resolver)
					if err != nil {
						return err
					}
					if !linkedOpen {
						err = a.closeWorkflowPreview(ctx, trigger)
						if err != nil {
							return err
						}
					}
				} else if event.HeadSHA != "" {
					if err := a.updateWorkflowPreviewHead(ctx, trigger, target.repository, event.HeadSHA, resolver, "webhook"); err != nil {
						return fmt.Errorf("update preview: %w", err)
					}
				}
			}
			return nil
		}
	}
	return nil
}

func (a *API) consumePolledComment(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, resolver events.GitHubResolver, groupService *groups.Service, eventService *events.Service) (result error) {
	if err := a.previewDeliveryActivity(ctx, target, event, "running", nil); err != nil {
		return err
	}
	defer func() {
		result = previewCommandError(event, result)
		state := "processed"
		if result != nil {
			state = "failed"
		}
		result = errors.Join(result, a.previewDeliveryActivity(ctx, target, event, state, result))
	}()
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		return err
	}
	if fields := strings.Fields(event.Arguments); len(fields) > 0 && fields[0] == "test" {
		return nil
	}
	runs, err := groupService.Process(ctx, event)
	if err != nil {
		return err
	}
	legacy, err := eventService.Process(ctx, event)
	if err != nil {
		return err
	}
	return a.previewLegacyLinks(ctx, target, event, runs, legacy)
}

func (a *API) previewLegacyLinks(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, runs []core.PreviewGroupRun, result core.EventResult) error {
	// Add environment URLs to delivery records without storing credentials or snapshots.
	rules, err := a.configuredEventRules(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		url := ""
		for _, run := range runs {
			if rule.ID == "group:"+run.GroupID {
				url = run.EntrypointURL
			}
		}
		for _, preview := range result.Previews {
			if rule.ID == "trigger:"+preview.TriggerID {
				url = preview.URL
			}
		}
		if url == "" || len(rule.ProjectIDs) != 1 {
			continue
		}
		item := core.EventActivity{ID: ulid.Make().String(), ProjectID: rule.ProjectIDs[0], RuleID: rule.ID, Name: rule.Name, Transport: target.transport, Kind: string(event.Kind), Repository: target.repository, PullRequest: event.PullRequestNumber, Command: event.Command, CommitSHA: event.HeadSHA, State: "running", PreviewURL: url, CreatedAt: time.Now().UTC()}
		if err := a.store.SaveEventActivity(ctx, previewDeliveryKey(event, rule.ID), item); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) consumeGitHubPreviewEvent(ctx context.Context, event core.IncomingEvent, groupService *groups.Service, eventService *events.Service) (result core.EventResult, resultErr error) {
	defer func() { resultErr = previewCommandError(event, resultErr) }()
	if err := a.processWorkflowPreviewWebhook(ctx, event); err != nil {
		return core.EventResult{}, err
	}
	if fields := strings.Fields(event.Arguments); event.Kind == core.EventKindPullRequestComment && len(fields) > 0 && fields[0] == "test" {
		return core.EventResult{Event: event}, nil
	}
	runs, err := groupService.Process(ctx, event)
	if err != nil {
		return core.EventResult{}, err
	}
	result, err = eventService.Process(ctx, event)
	if err != nil {
		return result, err
	}
	result.PreviewGroupRuns = runs
	target := &previewPollTarget{connectionID: event.ProviderConnectionID, repository: events.NormalizeRepository(event.Repository), transport: "webhook"}
	return result, a.previewLegacyLinks(ctx, target, event, runs, result)
}

func (a *API) previewHeadActivity(ctx context.Context, trigger core.WorkflowPreviewTrigger, resource core.WorkflowResource, refs map[string]string, state, message, revisionID string, transport []string) error {
	source, err := a.store.GetConfigSource(ctx, resource.ConfigSourceID)
	if err != nil {
		return err
	}
	ruleID, name := "preview:"+trigger.ID, resource.Name
	if trigger.TemplateID != "" {
		template, err := a.store.GetWorkflowPreviewTemplate(ctx, trigger.TemplateID)
		if err != nil {
			return err
		}
		ruleID, name = "template:"+template.ID, template.Name
	}
	mode := "poll"
	if len(transport) > 0 {
		mode = transport[0]
	}
	data, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	item := core.EventActivity{ID: ulid.Make().String(), ProjectID: source.ProjectID, RuleID: ruleID, Name: name, Transport: mode, Kind: "pull_request_update", Repository: trigger.Repository, PullRequest: trigger.PullRequestNumber, State: state, Message: message, ResourceID: resource.ID, PreviewURL: trigger.PreviewURL, CreatedAt: time.Now().UTC()}
	if revisionID != "" {
		item.RevisionIDs = []string{revisionID}
	}
	return a.store.SaveEventActivity(ctx, fmt.Sprintf("head:%s:%x", trigger.ID, digest), item)
}

func (a *API) consumePolledClosure(ctx context.Context, target *previewPollTarget, event core.IncomingEvent, resolver events.GitHubResolver, groupService *groups.Service, eventService *events.Service) (result error) {
	if err := a.previewDeliveryActivity(ctx, target, event, "running", nil); err != nil {
		return err
	}
	defer func() {
		state := "processed"
		if result != nil {
			state = "failed"
		}
		result = errors.Join(result, a.previewDeliveryActivity(ctx, target, event, state, result))
	}()
	if _, err := groupService.Process(ctx, event); err != nil {
		return err
	}
	if _, err := eventService.Process(ctx, event); err != nil {
		return err
	}
	for _, trigger := range target.workflowTriggers {
		if trigger.PullRequestNumber != event.PullRequestNumber {
			continue
		}
		linkedOpen, err := a.workflowPreviewLinkedPullRequestOpen(ctx, trigger, resolver)
		if err != nil {
			return err
		}
		if !linkedOpen {
			if err := a.closeWorkflowPreview(ctx, trigger); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validation errors may quote user input. Keep it out of the activity feed and poll errors.
type redactedPreviewError struct {
	cause   error
	message string
}

func (e *redactedPreviewError) Error() string { return e.message }
func (e *redactedPreviewError) Unwrap() error { return e.cause }
func previewCommandError(event core.IncomingEvent, err error) error {
	if err == nil || event.Arguments == "" {
		return err
	}
	message := err.Error()
	parts := strings.FieldsFunc(event.Arguments, func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == '=' })
	parts = append(parts, strings.FieldsFunc(event.Arguments, func(r rune) bool { return unicode.IsSpace(r) || r == ',' })...)
	parts = append(parts, event.Arguments)
	slices.SortFunc(parts, func(a, b string) int { return len(b) - len(a) })
	for _, part := range parts {
		if len(part) < 3 || part == "with" || part == "test" {
			continue
		}
		message = strings.ReplaceAll(message, strconv.Quote(part), "[argument omitted]")
		message = strings.ReplaceAll(message, part, "[argument omitted]")
	}
	return &redactedPreviewError{cause: err, message: message}
}

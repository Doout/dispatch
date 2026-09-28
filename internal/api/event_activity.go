package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
)

type eventRule struct {
	CanEditHooks       bool                `json:"canEditHooks,omitempty"`
	RepositoryProjects map[string][]string `json:"-"`
	ID                 string              `json:"id"`
	ProjectIDs         []string            `json:"-"`
	Name               string              `json:"name"`
	Kind               string              `json:"kind"`
	ConnectionID       string              `json:"-"`
	Repositories       []string            `json:"repositories"`
	Command            string              `json:"command,omitempty"`
	Branch             string              `json:"branch,omitempty"`
	Mode               string              `json:"mode"`
	Interval           int                 `json:"intervalSeconds,omitempty"`
	Enabled            bool                `json:"enabled"`
	PullRequest        int                 `json:"pullRequest,omitempty"`
	Check              *core.EventActivity `json:"check,omitempty"`
	Error              string              `json:"error,omitempty"`
	CheckKeys          []string            `json:"-"`
}

func previewCheckKey(connection, repository string) string {
	return "preview-check:" + connection + ":" + events.NormalizeRepository(repository)
}

func (a *API) configuredEventRules(ctx context.Context) ([]eventRule, error) {
	result := []eventRule{}
	sources, err := a.store.ListConfigSources(ctx)
	if err != nil {
		return nil, err
	}
	bySource := map[string]core.ConfigSource{}
	for _, source := range sources {
		bySource[source.ID] = source
		result = append(result, eventRule{ID: "configuration:" + source.ID, Name: source.Name, ProjectIDs: []string{source.ProjectID}, Kind: "configuration", Repositories: []string{source.Repository}, Branch: source.Branch, Mode: source.SyncMode, Interval: source.PollIntervalSeconds, Enabled: source.Active, Error: source.LastError, CheckKeys: []string{"configuration:" + source.ID}})
	}
	templates, err := a.store.ListWorkflowPreviewTemplates(ctx)
	if err != nil {
		return nil, err
	}
	for _, template := range templates {
		source, ok := bySource[template.ConfigSourceID]
		if !ok {
			continue
		}
		rule := eventRule{ID: "template:" + template.ID, Name: template.Name, ProjectIDs: []string{source.ProjectID}, Kind: "template", ConnectionID: template.GitHubAppID, Repositories: previewTemplateRepositories(template), Command: template.Command, Mode: "webhook_poll", Interval: 30, Enabled: template.Active}
		for _, repo := range rule.Repositories {
			rule.CheckKeys = append(rule.CheckKeys, previewCheckKey(rule.ConnectionID, repo))
		}
		if template.GitSource != nil {
			rule.Error = template.GitSource.LastError
			rule.CheckKeys = append(rule.CheckKeys, "template-sync:"+template.ID)
		}
		result = append(result, rule)
	}
	resources, err := a.store.ListWorkflowResources(ctx, "")
	if err != nil {
		return nil, err
	}
	byResource := map[string]core.WorkflowResource{}
	for _, resource := range resources {
		byResource[resource.ID] = resource
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		return nil, err
	}
	for _, trigger := range triggers {
		if trigger.ClosedAt != nil || trigger.TemplateID != "" {
			continue
		}
		resource, ok := byResource[trigger.ResourceID]
		if !ok {
			continue
		}
		source, ok := bySource[resource.ConfigSourceID]
		if !ok {
			continue
		}
		result = append(result, eventRule{ID: "preview:" + trigger.ID, Name: resource.Name, ProjectIDs: []string{source.ProjectID}, Kind: "preview", ConnectionID: trigger.GitHubAppID, Repositories: []string{trigger.Repository}, Command: trigger.Command, PullRequest: trigger.PullRequestNumber, Mode: "webhook_poll", Interval: 30, Enabled: resource.Active, CheckKeys: []string{previewCheckKey(trigger.GitHubAppID, trigger.Repository)}})
	}
	apps, err := a.store.ListAppsForUsage(ctx)
	if err != nil {
		return nil, err
	}
	byApp := map[string]core.App{}
	for _, app := range apps {
		byApp[app.ID] = app
	}
	legacy, err := a.store.ListEventTriggers(ctx, "")
	if err != nil {
		return nil, err
	}
	for _, trigger := range legacy {
		app, ok := byApp[trigger.AppID]
		if !ok {
			continue
		}
		result = append(result, eventRule{ID: "trigger:" + trigger.ID, Name: app.Name, ProjectIDs: []string{app.ProjectID}, Kind: "trigger", ConnectionID: trigger.GitHubAppID, Repositories: []string{trigger.Repository}, Command: trigger.Command, Mode: "webhook_poll", Interval: 30, Enabled: trigger.Enabled, CheckKeys: []string{previewCheckKey(trigger.GitHubAppID, trigger.Repository)}})
	}
	groups, err := a.store.ListPreviewGroups(ctx)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		rule := eventRule{ID: "group:" + group.ID, Name: group.Name, Kind: "group", ConnectionID: group.GitHubAppID, Command: group.Command, Mode: "webhook_poll", Interval: 30, Enabled: group.Enabled}
		rule.RepositoryProjects = map[string][]string{}
		for _, component := range group.Components {
			app, ok := byApp[component.AppID]
			if !ok {
				continue
			}
			if !slices.Contains(rule.ProjectIDs, app.ProjectID) {
				rule.ProjectIDs = append(rule.ProjectIDs, app.ProjectID)
			}
			rule.Repositories = append(rule.Repositories, component.Repository)
			repo := events.NormalizeRepository(component.Repository)
			rule.RepositoryProjects[repo] = append(rule.RepositoryProjects[repo], app.ProjectID)
			rule.CheckKeys = append(rule.CheckKeys, previewCheckKey(group.GitHubAppID, component.Repository))
		}
		if len(rule.ProjectIDs) > 0 {
			result = append(result, rule)
		}
	}
	connections, err := a.store.ListGitHubApps(ctx)
	if err != nil {
		return nil, err
	}
	webhookEnabled := map[string]bool{}
	for _, connection := range connections {
		webhookEnabled[connection.ID] = connection.WebhookSecretConfigured
	}
	for i := range result {
		rule := &result[i]
		if rule.Kind != "configuration" && !webhookEnabled[rule.ConnectionID] && a.eventConfig.WebhookSecret == "" {
			rule.Mode = "poll"
		}
	}
	return result, nil
}

func (a *API) listEventRules(w http.ResponseWriter, r *http.Request) {
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	rules, err := a.configuredEventRules(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	ids := []string{}
	for id := range visible {
		ids = append(ids, id)
	}
	checks, err := a.store.SearchEventActivity(r.Context(), core.EventActivitySearch{ProjectIDs: ids, ChecksOnly: true, Limit: 10000})
	if err != nil {
		a.internal(w, err)
		return
	}
	result := []eventRule{}
	for _, rule := range rules {
		allowed := len(rule.ProjectIDs) > 0
		for _, id := range rule.ProjectIDs {
			allowed = allowed && visible[id]
		}
		if !allowed {
			continue
		}
		for _, check := range checks {
			if !slices.Contains(rule.CheckKeys, check.RuleID) {
				continue
			}
			// Prefer a failure when one watched repository fails; otherwise show the oldest successful check.
			if rule.Check == nil || check.State == "failed" && rule.Check.State != "failed" || check.State == rule.Check.State && check.CreatedAt.Before(rule.Check.CreatedAt) {
				copy := check
				rule.Check = &copy
			}
		}
		if rule.Kind == "group" {
			rule.CanEditHooks = currentIdentity(r.Context()).SystemRole == core.UserRoleOwner
		}
		if rule.Kind == "trigger" {
			allowed, err := a.canProject(r.Context(), core.PermissionProjectConfigure, rule.ProjectIDs[0])
			if err != nil {
				a.internal(w, err)
				return
			}
			rule.CanEditHooks = allowed
		}
		result = append(result, rule)
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *API) listEventActivity(w http.ResponseWriter, r *http.Request) {
	visible, err := a.visibleProjectIDs(r.Context())
	if err != nil {
		a.internal(w, err)
		return
	}
	ids := []string{}
	for id := range visible {
		ids = append(ids, id)
	}
	transport := r.URL.Query().Get("transport")
	if transport != "" && !slices.Contains([]string{"poll", "webhook", "history"}, transport) {
		problem(w, 400, "Invalid transport", "Choose poll, webhook, or history.")
		return
	}
	before := r.URL.Query().Get("before")
	if len(before) > 64 || strings.ContainsAny(before, " \r\n\t") {
		problem(w, 400, "Invalid cursor", "Use the next cursor returned by this endpoint.")
		return
	}
	items, err := a.store.SearchEventActivity(r.Context(), core.EventActivitySearch{ProjectIDs: ids, Transport: transport, Before: before, Limit: 51})
	if err != nil {
		a.internal(w, err)
		return
	}
	next := ""
	if len(items) > 50 {
		items = items[:50]
		next = items[49].ID
	}
	count, err := a.store.CountEventActivity(r.Context(), core.EventActivitySearch{ProjectIDs: ids, Transport: transport})
	if err != nil {
		a.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items []core.EventActivity `json:"items"`
		Next  string               `json:"next,omitempty"`
		Total int                  `json:"total"`
	}{items, next, count})
}

package api

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/workflow"
)

const previewValuesDocument = `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: preview-42}
spec:
  sources:
    service: {repository: example/service, ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
  deployments:
    optimization:
      helm:
        sourceRef: service
        chartPath: charts/app
        releaseName: preview-42
        values:
          sourceCommit: "{{ sources.service.commit }}"
          wxo_optimization:
            configMap:
              data: {EXISTING_SETTING: retained}
`

const previewGatewayValuesComment = "/preview values\n```yaml\nwxo_optimization:\n  configMap:\n    data:\n      AGENT_GATEWAY_URL: http://agent-gateway.archer-server.svc.cluster.local\n```"

func previewValuesEvent(t *testing.T, id, body string) core.IncomingEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"action": "created", "repository": map[string]any{"full_name": "example/service"},
		"issue":   map[string]any{"number": 42, "pull_request": map[string]any{}},
		"comment": map[string]any{"id": json.Number(id), "body": body, "author_association": "MEMBER", "user": map[string]string{"login": "operator"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := events.ParseGitHubEvent("issue_comment", "delivery-"+id, raw, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	event.ProviderConnectionID = "qa-app"
	event.DeliveryID = events.CommentDeliveryID(event)
	event.HeadSHA = strings.Repeat("a", 40)
	return event
}

func TestWorkflowPreviewValuesPersistAcrossTransportsAndRedeploys(t *testing.T) {
	f := newFeedbackFixture(t)
	a, ctx := f.a, context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	resource, err := a.store.GetWorkflowResource(ctx, "qa-preview")
	must(err)
	resource.Document, resource.Path = previewValuesDocument, "temporary/preview.yaml"
	must(a.store.UpdateWorkflowResource(ctx, resource))
	head := strings.Repeat("a", 40)
	a.workflows.ResolvePreviewSource = func(_ context.Context, _ string, repository string, _ int) (githubapp.PullRequestHead, error) {
		return fixturePreviewSource(repository, head), nil
	}
	trigger := core.WorkflowPreviewTrigger{ID: "values-trigger", ResourceID: resource.ID, GitHubAppID: "qa-app", Repository: "example/service", PullRequestNumber: 42, Command: "/preview", CreatedAt: time.Now().UTC()}
	must(a.store.CreateWorkflowPreviewTrigger(ctx, trigger))
	target := &previewPollTarget{connectionID: "qa-app", repository: trigger.Repository, workflowTriggers: []core.WorkflowPreviewTrigger{trigger}, transport: "poll"}
	first := previewValuesEvent(t, "400", previewGatewayValuesComment)
	_, err = a.consumeGitHubPreviewEvent(ctx, first, a.groups, a.events)
	must(err)
	firstID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "400")
	must(err)
	if firstID == "" {
		t.Fatal("values comment did not start a revision")
	}
	firstRun, err := a.store.GetWorkflowRevision(ctx, firstID)
	must(err)
	want := first.PreviewValues.Values
	if !reflect.DeepEqual(firstRun.PreviewValues["optimization"], want) {
		t.Fatalf("comment values not captured: %+v", firstRun.PreviewValues)
	}
	expired, err := a.store.GetWorkflowResource(ctx, resource.ID)
	must(err)
	expired.Active, expired.State = false, "expired"
	must(a.store.UpdateWorkflowResource(ctx, expired))
	// Polling the same comment with another body must retain its original run.
	replayed := previewValuesEvent(t, "400", "/preview values\n```yaml\nreplayed: true\n```")
	must(a.consumePolledComment(ctx, target, replayed, events.GitHubResolver{}, a.groups, a.events))
	stillExpired, err := a.store.GetWorkflowResource(ctx, resource.ID)
	must(err)
	if stillExpired.State != "expired" || stillExpired.Active {
		t.Fatal("replaying the values comment renewed an expired preview")
	}
	bare := previewValuesEvent(t, "401", "/preview")
	must(a.consumePolledComment(ctx, target, bare, events.GitHubResolver{}, a.groups, a.events))
	bareID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "401")
	must(err)
	bareRun, err := a.store.GetWorkflowRevision(ctx, bareID)
	must(err)
	if !reflect.DeepEqual(bareRun.PreviewValues, firstRun.PreviewValues) {
		t.Fatal("bare redeploy lost overrides or replay changed them")
	}
	// Older unprocessed comments must not overwrite the newer configuration.
	stale := previewValuesEvent(t, "399", "/preview values\n```yaml\nstale: true\n```")
	must(a.processWorkflowPreviewComment(ctx, target, stale, events.GitHubResolver{}))
	staleID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "399")
	must(err)
	if staleID != "" {
		t.Fatal("an older comment started a deployment")
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	must(err)
	trigger = triggers[0]
	trigger.LiveReload = true
	must(a.store.UpdateWorkflowPreviewTrigger(ctx, trigger))
	head = strings.Repeat("b", 40)
	must(a.updateWorkflowPreviewHead(ctx, trigger, trigger.Repository, head, events.GitHubResolver{}))
	runs, err := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
	must(err)
	if runs[0].Trigger != "pull request update" || !reflect.DeepEqual(runs[0].PreviewValues, firstRun.PreviewValues) {
		t.Fatalf("automatic deployment lost values: %+v", runs[0])
	}
	clear := previewValuesEvent(t, "402", "/preview values clear")
	clear.HeadSHA = head
	must(a.consumePolledComment(ctx, target, clear, events.GitHubResolver{}, a.groups, a.events))
	clearID, err := a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, "402")
	must(err)
	cleared, err := a.store.GetWorkflowRevision(ctx, clearID)
	must(err)
	if len(cleared.PreviewValues) != 0 {
		t.Fatal("clear did not restore configured values")
	}
	historical, err := a.store.GetWorkflowRevision(ctx, firstID)
	must(err)
	if !reflect.DeepEqual(historical.PreviewValues, firstRun.PreviewValues) {
		t.Fatal("clear changed the historical snapshot")
	}
	current, err := a.store.GetWorkflowResource(ctx, resource.ID)
	must(err)
	if strings.Contains(current.Document, "AGENT_GATEWAY_URL") || !strings.Contains(current.Document, "EXISTING_SETTING") {
		t.Fatal("comment overrides changed the saved Application")
	}
}

func TestWorkflowPreviewValuesValidateBeforeCreatingTemplateInstance(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	template := core.WorkflowPreviewTemplate{ID: "values-template", ConfigSourceID: "qa-config", GitHubAppID: "qa-app", Name: "Values", Repository: "example/service", Command: "/preview", Document: strings.ReplaceAll(previewValuesDocument, "preview-42", "preview-__PREVIEW_ID__"), Active: true, CreatedAt: now, UpdatedAt: now}
	if err := f.a.store.CreateWorkflowPreviewTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	target := &previewPollTarget{connectionID: "qa-app", repository: template.Repository, workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	for _, test := range []struct{ body, want string }{
		{"/preview values clear", "no preview exists"},
		{"/preview values missing\n```yaml\nsetting: value\n```", "unknown Helm deployment"},
		{"/preview values\n```yaml\n- invalid\n```", "one YAML mapping"},
	} {
		event := previewValuesEvent(t, "500", test.body)
		if err := f.a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("wrong rejection for %q: %v", test.body, err)
		}
	}
	triggers, err := f.a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(triggers) != 0 {
		t.Fatalf("invalid command created a preview: %+v, %v", triggers, err)
	}
	resources, err := f.a.store.ListWorkflowResources(ctx, "qa-config")
	if err != nil || len(resources) != 1 {
		t.Fatalf("invalid command created a resource: %+v, %v", resources, err)
	}
	f.a.workflows.ResolvePreviewSource = func(_ context.Context, _ string, repository string, _ int) (githubapp.PullRequestHead, error) {
		return fixturePreviewSource(repository, strings.Repeat("a", 40)), nil
	}
	if err := f.a.processWorkflowPreviewComment(ctx, target, previewValuesEvent(t, "501", previewGatewayValuesComment), events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	triggers, err = f.a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(triggers) != 1 || len(triggers[0].PreviewValues["optimization"]) == 0 {
		t.Fatalf("first values comment did not create a configured preview: %+v, %v", triggers, err)
	}
	saved, err := f.a.store.GetWorkflowPreviewTemplate(ctx, template.ID)
	if err != nil || saved.Document != template.Document {
		t.Fatal("values comment modified the shared template")
	}
}

func TestWorkflowPreviewValuesSelectAndReplaceOnlyNamedDeployment(t *testing.T) {
	spec := &workflow.ApplicationSpec{Deployments: map[string]workflow.DeploymentSpec{"app": {}, "worker": {}}}
	trigger := core.WorkflowPreviewTrigger{Command: "/try", PreviewValues: map[string]map[string]any{"app": {"old": true}, "worker": {"retained": true}}}
	if _, err := prepareWorkflowPreviewValues(trigger, &core.WorkflowPreviewValuesCommand{Values: map[string]any{"new": true}}, spec); err == nil {
		t.Fatal("unqualified values were accepted for several deployments")
	}
	next, err := prepareWorkflowPreviewValues(trigger, &core.WorkflowPreviewValuesCommand{Deployment: "app", Values: map[string]any{"new": int64(9007199254740993)}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.PreviewValues["app"]) != 1 || next.PreviewValues["app"]["new"] != int64(9007199254740993) || next.PreviewValues["worker"]["retained"] != true || trigger.PreviewValues["app"]["old"] != true {
		t.Fatal("replacement changed another deployment or mutated the cached trigger")
	}
	cleared, err := prepareWorkflowPreviewValues(next, &core.WorkflowPreviewValuesCommand{Deployment: "app", Clear: true}, spec)
	if err != nil || len(cleared.PreviewValues) != 1 || cleared.PreviewValues["worker"]["retained"] != true {
		t.Fatalf("named clear lost another deployment: %+v, %v", cleared, err)
	}
}

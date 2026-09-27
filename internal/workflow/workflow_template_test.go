package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestWorkflowTemplateUsesInstanceAndTriggerVariables(t *testing.T) {
	input := `apiVersion: dispatch/v1alpha1
kind: WorkflowTemplate
metadata:
  name: app-{{instance.id}}
spec:
  sources:
    service:
      repository: "{{ trigger.repository }}"
  jobs:
    build:
      runFrom: service
      run: echo '{{ trigger.pullRequest.number }} {{ trigger.pullRequest.url }} {{ sources.service.commit }}'
`
	rendered, err := RenderWorkflowTemplate([]byte(input), TemplateVariables{ID: "42-unique", PRNumber: 42, Repository: "owner/service", PRURL: "https://github.example/owner/service/pull/42"})
	if err != nil {
		t.Fatal(err)
	}
	documents, err := Parse("instance.yaml", rendered)
	if err != nil || len(documents) != 1 {
		t.Fatalf("rendered = %s, error = %v", rendered, err)
	}
	document := documents[0]
	if document.Kind != KindApplication || document.Metadata.Name != "app-42-unique" || document.Spec.Sources["service"].Repository != "owner/service" {
		t.Fatalf("wrong instance: %+v", document)
	}
	run := document.Spec.Jobs["build"].Run
	if !strings.Contains(run, "42 https://github.example/owner/service/pull/42 {{ sources.service.commit }}") {
		t.Fatalf("wrong trigger/runtime variables: %s", run)
	}
	for _, invalid := range []string{
		strings.Replace(input, "instance.id", "instance.missing", 1),
		strings.Replace(input, "trigger.repository", "trigger.missing", 1),
		input + "---\nkind: Application\n",
	} {
		if _, err := RenderWorkflowTemplate([]byte(invalid), TemplateVariables{ID: "123", PRNumber: 123, Repository: "owner/service", PRURL: "https://example.test/pr/123"}); err == nil {
			t.Fatal("invalid template accepted")
		}
	}
	if _, err := RenderWorkflowTemplate([]byte(input), TemplateVariables{ID: "123"}); err == nil {
		t.Fatal("missing trigger context accepted")
	}
}

func TestWorkflowTemplateStagePromotion(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		failedCheck bool
		approval    bool
		deployments int
		stages      int
		state       string
	}{
		{name: "promote in order", deployments: 3, stages: 3, state: "succeeded"},
		{name: "failed check stops promotion", failedCheck: true, deployments: 1, stages: 1, state: "running"},
		{name: "approval pauses before staging", approval: true, deployments: 1, stages: 2, state: "awaiting_approval"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newWorkflowNoopFixture(t)
			ctx := context.Background()
			f.runner.unchanged = false
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, target := range []string{"dev", "staging", "production"} {
				server := f.server
				server.ID, server.Name = target, target
				must(f.data.CreateServer(ctx, server))
			}
			f.document.Metadata.Name = "app-{{ instance.id }}"
			deployment := f.document.Spec.Deployments["app"]
			deployment.Helm.ReleaseName = "app-{{ instance.id }}"
			f.document.Spec.Deployments["app"] = deployment
			f.document.Spec.Stages = []StageSpec{
				{Name: "development", TargetRef: "dev", Deploy: []string{"app"}, URL: "https://dev.example/preview/{{ instance.id }}", Approval: "automatic"},
				{Name: "staging", TargetRef: "staging", Deploy: []string{"app"}, Approval: "automatic"},
				{Name: "production", TargetRef: "production", Deploy: []string{"app"}, Approval: "automatic"},
			}
			if scenario.failedCheck {
				f.document.Spec.Stages[0].Checks = map[string]CheckSpec{"e2e": {PipelineRef: "unavailable-check", With: map[string]string{"base-url": "{{ stage.url }}"}}}
			}
			if scenario.approval {
				f.document.Spec.Stages[1].Approval = "required"
			}
			raw, err := f.document.MarshalYAML()
			must(err)
			template := strings.Replace(string(raw), "kind: Application", "kind: WorkflowTemplate", 1)
			template = strings.Replace(template, "spec:\n", "spec:\n    triggers:\n        pullRequestComment:\n            sources: [service]\n            command: /preview\n", 1)
			rendered, err := RenderWorkflowTemplate([]byte(template), TemplateVariables{ID: "42"})
			must(err)
			documents, err := Parse("generated.yaml", rendered)
			must(err)
			document := documents[0]
			if len(document.Spec.Stages) != 3 || document.Spec.Stages[0].URL != "https://dev.example/preview/42" {
				t.Fatal("template lost its stages or instance URL")
			}
			if scenario.failedCheck && document.Spec.Stages[0].Checks["e2e"].With["base-url"] != "{{ stage.url }}" {
				t.Fatal("stage runtime variable was changed during instantiation")
			}
			revision := core.WorkflowRevision{ID: "promotion", ResourceID: f.resource.ID, State: "running", Trigger: "manual", SpecDigest: f.resource.SpecDigest, Sources: f.snapshot, Outputs: f.previous.Outputs, CreatedAt: time.Now().UTC()}
			must(f.data.CreateWorkflowRevision(ctx, revision))
			err = f.service.runStages(ctx, f.resource, f.source, document, &revision, 0)
			if scenario.failedCheck {
				if err == nil || !strings.Contains(err.Error(), "check e2e") {
					t.Fatalf("check failure not returned: %v", err)
				}
			} else {
				must(err)
			}
			runs, err := f.data.ListWorkflowStageRuns(ctx, revision.ID)
			must(err)
			if len(runs) != scenario.stages || f.runner.starts != scenario.deployments || revision.State != scenario.state {
				t.Fatalf("promotion state: runs=%+v deployments=%d revision=%s", runs, f.runner.starts, revision.State)
			}
			byName := map[string]core.WorkflowStageRun{}
			for _, run := range runs {
				byName[run.StageName] = run
				for _, id := range run.DeploymentIDs {
					deployed, err := f.data.GetDeployment(ctx, id)
					must(err)
					app, err := f.data.GetApp(ctx, deployed.AppID)
					must(err)
					if app.ServerID != run.TargetRef || deployed.CommitSHA != f.snapshot["gitops"].CommitSHA || !strings.Contains(app.HelmValues, "built-image") {
						t.Fatal("promotion changed the target, pinned chart, or built image")
					}
				}
			}
			if scenario.failedCheck && byName["development"].State != "failed" {
				t.Fatal("failed check was not recorded")
			}
			if scenario.approval && byName["staging"].State != "awaiting_approval" {
				t.Fatal("staging did not wait for approval")
			}
			if !scenario.failedCheck && !scenario.approval {
				for index, name := range []string{"staging", "production"} {
					previous := byName[[]string{"development", "staging"}[index]]
					current := byName[name]
					if previous.FinishedAt == nil || current.StartedAt == nil || current.StartedAt.Before(*previous.FinishedAt) {
						t.Fatal("stages ran out of order")
					}
				}
			}
		})
	}
}

package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestPreviewChecksRunOnlyOnCommandWithoutRedeploying(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	f.resource.Temporary = true
	f.document.Spec.Stages[0].URL = "https://preview.example.test/42"
	f.document.Spec.Stages[0].Checks = map[string]CheckSpec{
		"health": {PipelineRef: "preview-qa", With: map[string]string{"url": "{{ stage.url }}"}},
		"qa":     {PipelineRef: "preview-qa", When: "onDemand", With: map[string]string{"url": "{{ stage.url }}"}},
	}
	f.document.Spec.Stages = append(f.document.Spec.Stages, StageSpec{Name: "staging", TargetRef: "Target", Approval: "required", Deploy: []string{"app"}})
	encoded, err := f.document.MarshalYAML()
	must(err)
	f.resource.Document = string(encoded)
	f.resource.SpecDigest, err = f.document.Digest()
	must(err)
	must(f.data.UpdateWorkflowResource(ctx, f.resource))
	pipelineDocument := `apiVersion: dispatch/v1alpha1
kind: Pipeline
metadata:
  name: preview-qa
spec:
  inputs:
    url: {type: string, required: true}
  jobs:
    verify:
      run: test "{{ inputs.url }}" = "https://preview.example.test/42"
`
	if _, err := Parse("pipeline.yaml", []byte(pipelineDocument)); err != nil {
		t.Fatal(err)
	}
	must(f.data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "qa-pipeline", ConfigSourceID: f.source.ID, APIVersion: APIVersion, Kind: KindPipeline, Name: "preview-qa", Path: "pipeline.yaml", Document: pipelineDocument, Active: true, State: "ready", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}))
	deployment := core.WorkflowRevision{ID: "deployed-preview", ResourceID: f.resource.ID, State: "running", Trigger: "pull request comment 1", Sources: f.snapshot, Outputs: f.previous.Outputs, CreatedAt: time.Now().UTC()}
	must(f.data.CreateWorkflowRevision(ctx, deployment))
	must(f.service.runStages(ctx, f.resource, f.source, f.document, &deployment, 0))
	stages, err := f.data.ListWorkflowStageRuns(ctx, deployment.ID)
	must(err)
	if len(stages) != 2 || stages[0].CheckRuns["health"] == "" || stages[0].CheckRuns["qa"] != "" || stages[1].State != "awaiting_approval" {
		t.Fatalf("deployment did not keep checks separate: %+v", stages)
	}
	starts := f.runner.starts
	testRun, err := f.service.StartPreviewChecks(ctx, f.resource.ID, "comment-2")
	must(err)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result, err := f.data.GetWorkflowRevision(ctx, testRun.ID)
		must(err)
		if result.State == "succeeded" {
			break
		}
		if result.State == "failed" || result.State == "cancelled" {
			t.Fatalf("on-demand check failed: %+v", result)
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err := f.data.GetWorkflowRevision(ctx, testRun.ID)
	must(err)
	if result.State != "succeeded" || result.Sources["service"].CommitSHA != deployment.Sources["service"].CommitSHA || f.runner.starts != starts {
		t.Fatalf("test rebuilt or redeployed preview: result=%+v deployments=%d", result, f.runner.starts-starts)
	}
	stillWaiting, err := f.data.GetWorkflowRevision(ctx, deployment.ID)
	must(err)
	if stillWaiting.State != "awaiting_approval" {
		t.Fatalf("on-demand check cancelled the staged deployment: %+v", stillWaiting)
	}
	stages, err = f.data.ListWorkflowStageRuns(ctx, testRun.ID)
	must(err)
	if len(stages) != 1 || stages[0].State != "succeeded" || stages[0].CheckRuns["qa"] == "" || stages[0].CheckRuns["health"] != "" || len(stages[0].DeploymentIDs) != 0 {
		t.Fatalf("on-demand check did not record its pipeline run: %+v", stages)
	}
	pipeline, err := f.data.GetWorkflowRevision(ctx, stages[0].CheckRuns["qa"])
	must(err)
	if pipeline.State != "succeeded" || !strings.Contains(pipeline.Trigger, "/qa") {
		t.Fatalf("pipeline did not run against the preview URL: %+v", pipeline)
	}
}

func TestPreviewFeedbackKeepsDeployedLinksAndPolicy(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	f.resource.Temporary = true
	now := time.Now().UTC()
	if err := f.data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "app", Name: "App", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	trigger := core.WorkflowPreviewTrigger{ID: "feedback-trigger", ResourceID: f.resource.ID, GitHubAppID: "app", Repository: "example/service", PullRequestNumber: 42, LinkedPullRequests: map[string]int{"ui": 84}, PreviewURL: "https://preview.example.test/42", CreatedAt: now}
	if err := f.data.CreateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	f.snapshot["ui"] = core.WorkflowSourceRevision{Alias: "ui", Repository: "example/ui", CommitSHA: strings.Repeat("b", 40)}
	targets, err := f.service.previewPullRequests(ctx, f.resource, f.snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Number != 42 || targets[1].Number != 84 {
		t.Fatalf("wrong snapshot: %+v", targets)
	}
	deployed := core.WorkflowRevision{ID: "deployed", ResourceID: f.resource.ID, State: "succeeded", Sources: f.snapshot, PullRequests: targets, CreatedAt: now}
	if err := f.data.CreateWorkflowRevision(ctx, deployed); err != nil {
		t.Fatal(err)
	}
	if err := f.data.UpdateWorkflowPreviewTriggerLinks(ctx, trigger.ID, map[string]int{"ui": 85}); err != nil {
		t.Fatal(err)
	}
	f.document.Spec.Reporting = &core.WorkflowReporting{StatusContext: "Dispatch/qa", ReviewOnSuccess: "approve", ReviewOnFailure: "requestChanges"}
	feedback, err := f.service.previewFeedback(ctx, f.resource, deployed, f.document)
	if err != nil {
		t.Fatal(err)
	}
	if feedback.DeploymentID != "deployed" || feedback.Targets[1].Number != 84 || feedback.Targets[1].CommitSHA != strings.Repeat("b", 40) || feedback.ReviewOnSuccess != "approve" {
		t.Fatalf("used current links instead of deployed links: %+v", feedback)
	}
	deployed.PullRequests = nil
	legacy, err := f.service.previewFeedback(ctx, f.resource, deployed, f.document)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range legacy.Targets {
		if target.Review != "skipped" || target.SkipReason == "" {
			t.Fatalf("legacy deployment permitted a review without link snapshot: %+v", target)
		}
	}
	f.document.Spec.Reporting = nil
	feedback, err = f.service.previewFeedback(ctx, f.resource, deployed, f.document)
	if err != nil {
		t.Fatal(err)
	}
	if feedback.ReviewOnSuccess != "" || feedback.ReviewOnFailure != "" || feedback.StatusContext != "Dispatch/preview-tests/"+f.resource.Name {
		t.Fatalf("reviews unexpectedly enabled: %+v", feedback)
	}
}

func TestWorkflowReportingValidation(t *testing.T) {
	base := `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: preview}
spec:
  sources: {service: {repository: example/service, branch: main}}
  reporting:
`
	for _, policy := range []string{"    reviewOnSuccess: requestChanges\n", "    reviewOnFailure: approve\n", "    statusContext: Dispatch/deployment\n", "    reviewOnSuccess: merge\n", "    unknownOption: approve\n"} {
		if _, err := Parse("preview.yaml", []byte(base+policy)); err == nil {
			t.Fatalf("unsafe/unknown reporting policy accepted: %s", policy)
		}
	}
	if _, err := Parse("preview.yaml", []byte(base+"    statusContext: Dispatch/qa\n    reviewOnSuccess: approve\n    reviewOnFailure: requestChanges\n")); err != nil {
		t.Fatal(err)
	}
}

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

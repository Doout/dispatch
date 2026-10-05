package api

import (
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflow"
)

func TestWorkflowPreviewValuesDeploymentNamedClear(t *testing.T) {
	spec := &workflow.ApplicationSpec{Deployments: map[string]workflow.DeploymentSpec{"clear": {}, "worker": {}}}
	trigger := core.WorkflowPreviewTrigger{Command: "/preview", PreviewValues: map[string]map[string]any{"worker": {"retained": true}}}
	event := previewValuesEvent(t, "950", "/preview values clear\n```yaml\nsetting: literal\n```")
	if event.PreviewValuesError != "" || event.PreviewValues == nil {
		t.Fatalf("deployment named clear must accept values: %#v", event)
	}
	next, err := prepareWorkflowPreviewValues(trigger, event.PreviewValues, spec)
	if err != nil || next.PreviewValues["clear"]["setting"] != "literal" || next.PreviewValues["worker"]["retained"] != true {
		t.Fatalf("values did not target the clear deployment: %+v, %v", next, err)
	}
	namedClear := previewValuesEvent(t, "951", "/preview values clear clear")
	next, err = prepareWorkflowPreviewValues(next, namedClear.PreviewValues, spec)
	if err != nil || len(next.PreviewValues) != 1 || next.PreviewValues["worker"]["retained"] != true {
		t.Fatalf("named clear removed another deployment's values: %+v, %v", next, err)
	}
	clearAll := previewValuesEvent(t, "952", "/preview values clear")
	next, err = prepareWorkflowPreviewValues(next, clearAll.PreviewValues, spec)
	if err != nil || len(next.PreviewValues) != 0 {
		t.Fatalf("clear without YAML must still reset every deployment: %+v, %v", next, err)
	}
}

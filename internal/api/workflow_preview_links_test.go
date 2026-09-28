package api

import (
	"context"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflow"
)

func TestWorkflowPreviewLinksPinBothPullRequestHeads(t *testing.T) {
	resource := core.WorkflowResource{Path: "temporary.yaml", Document: `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: preview-42
spec:
  sources:
    service:
      repository: Example/service
      ref: previous-service
    ui:
      repository: Example/ui
      ref: previous-ui
`}
	documents, err := workflow.Parse(resource.Path, []byte(resource.Document))
	if err != nil {
		t.Fatal(err)
	}
	links, err := parseWorkflowPreviewLinks("with ui=#123", documents[0].Spec.Sources, "Example/service", 42)
	if err != nil || links["ui"].Number != 123 || links["ui"].Remove {
		t.Fatalf("links=%v err=%v", links, err)
	}
	updated, err := pinWorkflowPreviewSources(resource, map[string]string{"service": "service-head", "ui": "ui-head"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.SpecDigest == "" || !strings.Contains(updated.Document, "service-head") || !strings.Contains(updated.Document, "ui-head") {
		t.Fatalf("source heads were not pinned: %+v", updated)
	}
	bare, err := parseWorkflowPreviewLinks("", documents[0].Spec.Sources, "Example/service", 42)
	if err != nil || len(bare) != 0 {
		t.Fatalf("bare command changed links: %v, %v", bare, err)
	}
	for _, arguments := range []string{"with ui=#0", "with unknown=#12", "with ui=#12,example/ui=#13", "with service=#99"} {
		if _, err := parseWorkflowPreviewLinks(arguments, documents[0].Spec.Sources, "Example/service", 42); err == nil {
			t.Fatalf("accepted invalid linked PR %q", arguments)
		}
	}
}

func TestWorkflowPreviewUnlinkArguments(t *testing.T) {
	sources := map[string]workflow.SourceSpec{
		"service": {Repository: "example/service"},
		"ui":      {Repository: "example/ui"},
		"worker":  {Repository: "other/worker"},
	}
	for _, arguments := range []string{"without ui", "without example/ui", "WITHOUT UI", "without ui=#26"} {
		changes, err := parseWorkflowPreviewLinks(arguments, sources, "example/service", 3)
		if err != nil || len(changes) != 1 || !changes["ui"].Remove {
			t.Fatalf("%q: changes=%v err=%v", arguments, changes, err)
		}
	}
	changes, err := parseWorkflowPreviewLinks("without ui,worker", sources, "example/service", 3)
	if err != nil || len(changes) != 2 || !changes["ui"].Remove || !changes["worker"].Remove {
		t.Fatalf("multiple removals: %v, %v", changes, err)
	}
	for _, arguments := range []string{"without", "without unknown", "without service", "without service=#3", "without ui=#0", "without ui=#no", "without ui=", "without ui,example/ui", "without ui with worker=#4"} {
		if _, err := parseWorkflowPreviewLinks(arguments, sources, "example/service", 3); err == nil {
			t.Errorf("accepted %q", arguments)
		}
	}
}

func TestWorkflowPreviewUnlinkRestoresPinnedDefaultAndKeepsOtherLinks(t *testing.T) {
	resource := core.WorkflowResource{Path: "temporary.yaml", Document: `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: preview-3
spec:
  sources:
    service:
      repository: example/service
      ref: service-head
    ui:
      repository: example/ui
      ref: ui-pr-head
    worker:
      repository: example/worker
      ref: worker-pr-head
`}
	trigger := core.WorkflowPreviewTrigger{
		LinkedPullRequests: map[string]int{"ui": 26, "worker": 8},
		SourceDefaults:     map[string]core.WorkflowPreviewSourceDefault{"ui": {Repository: "example/ui", Ref: "release-v2"}},
	}
	a := &API{}
	changes := map[string]workflowPreviewLinkChange{"ui": {Remove: true, Number: 26}}
	updated, next, err := a.prepareWorkflowPreviewLinks(context.Background(), resource, trigger, changes)
	if err != nil {
		t.Fatal(err)
	}
	documents, err := workflow.Parse(updated.Path, []byte(updated.Document))
	if err != nil || documents[0].Spec.Sources["ui"].Ref != "release-v2" || documents[0].Spec.Sources["worker"].Ref != "worker-pr-head" || documents[0].Spec.Sources["service"].Ref != "service-head" {
		t.Fatalf("incorrect restored sources: %s, %v", updated.Document, err)
	}
	if next.LinkedPullRequests["ui"] != 0 || next.LinkedPullRequests["worker"] != 8 || trigger.LinkedPullRequests["ui"] != 26 {
		t.Fatalf("unlink modified unrelated or original links: %+v", next)
	}
	if _, _, err := a.prepareWorkflowPreviewLinks(context.Background(), resource, trigger, map[string]workflowPreviewLinkChange{"ui": {Remove: true, Number: 27}}); err == nil {
		t.Fatal("unlinked a different PR")
	}
	if _, _, err := a.prepareWorkflowPreviewLinks(context.Background(), updated, next, changes); err != nil {
		t.Fatalf("repeated unlink failed: %v", err)
	}
	trigger.SourceDefaults["ui"] = core.WorkflowPreviewSourceDefault{Repository: "other/ui", Branch: "main"}
	if _, _, err := a.prepareWorkflowPreviewLinks(context.Background(), resource, trigger, changes); err == nil {
		t.Fatal("restored defaults from a different repository")
	}
	trigger.SourceDefaults = nil
	if _, _, err := a.prepareWorkflowPreviewLinks(context.Background(), resource, trigger, changes); err == nil {
		t.Fatal("guessed a branch for a legacy one-off preview")
	}
}

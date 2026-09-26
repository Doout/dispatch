package api

import (
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
	if err != nil || links["ui"] != 123 {
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

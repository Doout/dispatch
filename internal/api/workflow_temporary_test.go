package api

import (
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflow"
)

func TestTemporaryPreviewIDKeepsPRNumberAndAvoidsReleaseCollision(t *testing.T) {
	template := `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: dev-preview-__PREVIEW_ID__
spec:
  sources:
    chart:
      repository: example/chart
  deployments:
    preview:
      helm:
        sourceRef: chart
        namespace: preview-apps
        releaseName: preview-__PREVIEW_ID__
        values:
          uiName: web-ui-preview-__PREVIEW_ID__
  stages:
    - name: development
      targetRef: dev
      deploy: [preview]
`
	first, err := renderTemporaryPreviewID(template, "42", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "name: dev-preview-42\n") || !strings.Contains(first, "releaseName: preview-42\n") {
		t.Fatalf("first preview did not use the PR number: %s", first)
	}
	existing := []core.WorkflowResource{{Name: "different-resource", Path: "existing.yaml", Document: first, State: "ready"}}
	second, err := renderTemporaryPreviewID(template, "42", existing)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := workflow.Parse("second.yaml", []byte(second))
	if err != nil {
		t.Fatal(err)
	}
	name := parsed[0].Metadata.Name
	if !strings.HasPrefix(name, "dev-preview-42-") || name == "dev-preview-42" {
		t.Fatalf("collision did not receive a random suffix: %s", name)
	}
	if !strings.Contains(second, "releaseName: preview-42-") || !strings.Contains(second, "uiName: web-ui-preview-42-") {
		t.Fatalf("preview identity did not share the suffix: %s", second)
	}
}

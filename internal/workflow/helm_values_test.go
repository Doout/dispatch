package workflow

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	helmvalues "helm.sh/helm/v3/pkg/cli/values"
)

func TestHelmFilesMergeWithoutRenderingExpressions(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "base.yaml")
	second := filepath.Join(dir, "slot.yaml")
	os.WriteFile(first, []byte("app:\n  port: 80\n  list: [a, b]\n  remove: base\n  expression: '{{ .Release.Name }}'\n"), 0600)
	os.WriteFile(second, []byte("app:\n  port: 90\n  list: [c]\n  remove: null\n"), 0600)
	_, digest, err := readHelmValues(first)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = readHelmValues(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatal("missing content hash")
	}
	merged, err := (&helmvalues.Options{ValueFiles: []string{first, second}}).MergeValues(nil)
	if err != nil {
		t.Fatal(err)
	}
	app := merged["app"].(map[string]interface{})
	if app["expression"] != "{{ .Release.Name }}" || app["remove"] != nil || len(app["list"].([]interface{})) != 1 {
		t.Fatalf("incorrect merge: %#v", app)
	}
	// Helm removes null overrides when it coalesces chart defaults.
	got, err := chartutil.CoalesceValues(&chart.Chart{Metadata: &chart.Metadata{Name: "test"}, Values: map[string]interface{}{"app": map[string]interface{}{"remove": "default"}}}, merged)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := got["app"].(map[string]interface{})["remove"]; exists {
		t.Fatal("null did not delete default")
	}
	before := app["port"]
	if err := setNestedValue(merged, "app.port", "bound-image-output"); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(before, app["port"]) {
		t.Fatal("binding failed to override file")
	}
}

func TestValuesPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "values.yaml"), []byte("x: y"), 0600)
	os.Symlink(outside, filepath.Join(root, "escape"))
	if _, err := containedPath(root, filepath.Join(root, "escape", "values.yaml")); err == nil {
		t.Fatal("accepted symlink escape")
	}
	if _, err := safeJoin(root, "../values.yaml"); err == nil {
		t.Fatal("accepted parent path")
	}
	if _, _, err := readHelmValues(root); err == nil {
		t.Fatal("accepted directory")
	}
	if _, _, err := readHelmValues(filepath.Join(root, "missing")); err == nil {
		t.Fatal("accepted missing file")
	}
}

func TestNewHelmInputsValidation(t *testing.T) {
	const document = `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: test
spec:
  sources:
    gitops:
      repository: owner/gitops
      ref: 8b88a99188ca7c7d09fa68c0cdc05a87fcfd5dda
    slots:
      repository: owner/slots
  deployments:
    test:
      helm:
        sourceRef: gitops
        chartPath: charts/app
        valuesFiles:
          - sourceRef: slots
            path: values/slot.yaml
`
	docs, err := Parse("application.yaml", []byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if docs[0].Spec.Sources["gitops"].Branch != "" {
		t.Fatal("default branch conflicts with ref")
	}
	for _, broken := range []string{
		strings.Replace(document, "path: values/slot.yaml", "path: ../escape.yaml", 1),
		strings.Replace(document, "sourceRef: slots", "sourceRef: missing", 1),
		strings.Replace(document, "chartPath: charts/app", "chartPath: /etc", 1),
		strings.Replace(document, "ref: 8b88", "branch: main\n      ref: 8b88", 1),
	} {
		if _, err := Parse("application.yaml", []byte(broken)); err == nil {
			t.Fatal("accepted invalid Helm source input")
		}
	}
}

func TestPinnedRefNeedsNoBranchHeadLookup(t *testing.T) {
	sha := "8b88a99188ca7c7d09fa68c0cdc05a87fcfd5dda"
	got, err := (&Service{}).repositoryHead(context.Background(), core.ConfigSource{}, "owner/repo", sha)
	if err != nil || got != sha {
		t.Fatalf("got %q: %v", got, err)
	}
}

func TestDeploymentInlineValuesKeepRuntimeBehavior(t *testing.T) {
	spec := HelmDeploymentSpec{Values: map[string]any{"revision": "{{ sources.app.commit }}", "list": []any{"one"}}}
	got, evidence, err := (&Service{}).deploymentValues(context.Background(), core.ConfigSource{}, core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{"app": {CommitSHA: "recorded"}}}, StageSpec{}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got["revision"] != "recorded" || len(evidence) != 1 {
		t.Fatalf("unexpected inline values: %v", got)
	}
}

func TestValueSourcesFollowOverrides(t *testing.T) {
	origins := map[string]string{}
	recordValueSources(origins, "", map[string]any{"image": map[string]any{"tag": "old", "registry": "registry"}, "password": "private-value"}, "platform.yaml")
	recordValueSources(origins, "", map[string]any{"image": map[string]any{"tag": "new"}}, "slot.yaml")
	if origins["image.tag"] != "slot.yaml" || origins["image.registry"] != "platform.yaml" {
		t.Fatal(origins)
	}
	recordValueSources(origins, "", map[string]any{"image": nil}, "inline")
	if _, ok := origins["image.tag"]; ok {
		t.Fatal("stale nested source")
	}
	for _, source := range origins {
		if source == "private-value" {
			t.Fatal("value stored in source metadata")
		}
	}
}

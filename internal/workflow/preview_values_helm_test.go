package workflow

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/chartutil"
)

func TestPreviewHelmValuesRemainLiteralAndPreserveBuildBindings(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	spec := f.document.Spec.Deployments["app"]
	spec.Helm.Values["config"] = map[string]any{"inherited": "keep", "remove": "default", "list": []any{"a", "b"}, "expression": "{{ sources.service.commit }}"}
	spec.Helm.Values["wxo_optimization"] = map[string]any{"configMap": map[string]any{"data": map[string]any{"OTHER_KEY": "keep", "AGENT_GATEWAY_URL": "inherited"}}}
	revision := f.previous
	revision.PreviewValues = map[string]map[string]any{
		"app": {
			"image":            map[string]any{"tag": "must-not-replace-build-output"},
			"config":           map[string]any{"remove": nil, "list": []any{"c"}, "expression": "{{ sources.service.commit }}", "large": int64(9007199254740993), "unsigned": uint64(18446744073709551615), "numericText": "9007199254740993"},
			"wxo_optimization": map[string]any{"configMap": map[string]any{"data": map[string]any{"AGENT_GATEWAY_URL": "https://preview-109.example.test"}}},
		},
		"unrelated": {"image": map[string]any{"tag": "other-deployment"}},
	}
	prepared, err := f.service.prepareHelmDeployment(context.Background(), f.resource, f.source, revision, f.document.Spec.Stages[0], "app", spec, f.server)
	if err != nil {
		t.Fatal(err)
	}
	values, err := chartutil.ReadValues([]byte(prepared.app.HelmValues))
	if err != nil {
		t.Fatal(err)
	}
	image := values["image"].(map[string]any)
	if image["tag"] != revision.Outputs["build"]["image"] {
		t.Fatal("preview settings replaced immutable build output", image)
	}
	config := values["config"].(map[string]any)
	if config["inherited"] != "keep" || config["remove"] != nil || config["expression"] != "{{ sources.service.commit }}" || len(config["list"].([]any)) != 1 {
		t.Fatal("literal Helm overlay changed merge behavior", config)
	}
	// Use the deployment's YAML decoder rather than Helm's float64 file reader
	// to verify exactly what the controller handed to the deployment service.
	var persisted map[string]any
	if err := yaml.Unmarshal([]byte(prepared.app.HelmValues), &persisted); err != nil {
		t.Fatal(err)
	}
	numeric := persisted["config"].(map[string]any)
	if _, quoted := numeric["large"].(string); quoted || fmt.Sprint(numeric["large"]) != "9007199254740993" {
		t.Fatal("final Helm YAML rounded or quoted a literal integer", numeric)
	}
	if numeric["unsigned"] != uint64(18446744073709551615) || numeric["numericText"] != "9007199254740993" {
		t.Fatal("final Helm YAML changed unsigned or string scalar types", numeric)
	}
	data := values["wxo_optimization"].(map[string]any)["configMap"].(map[string]any)["data"].(map[string]any)
	if data["AGENT_GATEWAY_URL"] != "https://preview-109.example.test" || data["OTHER_KEY"] != "keep" {
		t.Fatal("preview gateway override lost inherited values", data)
	}
	if prepared.chart.CommitSHA != revision.Sources[spec.Helm.SourceRef].CommitSHA || prepared.app.HelmRelease != f.app.HelmRelease || prepared.app.HelmNamespace != f.app.HelmNamespace {
		t.Fatal("value override changed chart/release identity", prepared)
	}
	evidence := strings.Join(prepared.evidence, "\n")
	if strings.Contains(evidence, "https://preview-109.example.test") || !strings.Contains(evidence, `"wxo_optimization.configMap.data.AGENT_GATEWAY_URL":"Preview values"`) || !strings.Contains(evidence, `"image.tag":"Build output: build.image"`) {
		t.Fatal("value provenance leaked values or omitted source paths", evidence)
	}
	if spec.Helm.Values["config"].(map[string]any)["remove"] != "default" || revision.PreviewValues["app"]["image"].(map[string]any)["tag"] != "must-not-replace-build-output" {
		t.Fatal("Helm preparation mutated a stored input map")
	}
}

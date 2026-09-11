package chartvalues

import (
	"testing"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
)

func TestPreviewTplGatewayValues(t *testing.T) {
	for _, regulated := range []bool{false, true} {
		c := &chart.Chart{Metadata: &chart.Metadata{Name: "gateway"}, Templates: []*chart.File{
			{Name: "templates/configmap.yaml", Data: []byte(`{{ range $key, $value := .Values.configMap.data }}{{ tpl (toString $value) $ | quote }}{{ end }}`)},
		}}
		values := map[string]any{
			"cluster":       map[string]any{"hipaa_env": regulated, "fedramp_env": false},
			"archer_server": map[string]any{"env": map[string]any{"ai_gateway_base_url": "https://gateway.example"}, "model_maps": map[string]any{"default_llm_model": "watsonx/granite"}},
			"configMap": map[string]any{"data": map[string]any{
				"URL":      `{{ .Values.archer_server.env.ai_gateway_base_url }}`,
				"MODEL":    `{{- if and (not .Values.cluster.hipaa_env) (not .Values.cluster.fedramp_env) -}}azure-openai/gpt-5.4{{- else -}}{{ .Values.archer_server.model_maps.default_llm_model }}{{- end -}}`,
				"PROVIDER": `{{- if and (not .Values.cluster.hipaa_env) (not .Values.cluster.fedramp_env) -}}azure-openai{{- else -}}{{ (splitList "/" .Values.archer_server.model_maps.default_llm_model) | first }}{{- end -}}`,
			}},
			"literal": `{{ .Values.archer_server.env.ai_gateway_base_url }}`,
		}
		result, err := Analyze(c, values)
		if err != nil {
			t.Fatal(err)
		}
		PreviewTpl(c, values, chartutil.ReleaseOptions{Name: "slot1", Namespace: "slots"}, &result)
		model, provider := "azure-openai/gpt-5.4", "azure-openai"
		if regulated {
			model, provider = "watsonx/granite", "watsonx"
		}
		for path, want := range map[string]string{"configMap.data.URL": "https://gateway.example", "configMap.data.MODEL": model, "configMap.data.PROVIDER": provider} {
			if got := result.Rendered[path]; got != want {
				t.Fatalf("%s: got %q, want %q", path, got, want)
			}
		}
		if _, ok := result.Rendered["literal"]; ok {
			t.Fatal("rendered a value that does not reach tpl")
		}
	}
}

func TestPreviewTplPreservesFailuresAndSupportsHelpers(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "preview"}, Templates: []*chart.File{
		{Name: "templates/config.yaml", Data: []byte(`{{ range .Values.data }}{{ tpl . $ }}{{ end }}`)},
		{Name: "templates/_helpers.tpl", Data: []byte(`{{ define "name" }}{{ .Release.Name }}{{ end }}`)},
	}}
	values := map[string]any{"data": map[string]any{
		"helper": `{{ include "name" . }}`,
		"broken": `{{ .Values.missing.field }}`,
		"empty":  `{{ "" }}`,
		"masked": `{{ .Values.password }}`,
	}, "password": "••••••••"}
	result, err := Analyze(c, values)
	if err != nil {
		t.Fatal(err)
	}
	PreviewTpl(c, values, chartutil.ReleaseOptions{Name: "slot1"}, &result)
	if result.Rendered["data.helper"] != "slot1" || result.Rendered["data.masked"] != "••••••••" {
		t.Fatal(result.Rendered)
	}
	if _, ok := result.Rendered["data.empty"]; !ok {
		t.Fatal("empty result lost")
	}
	if _, ok := result.Rendered["data.broken"]; ok {
		t.Fatal("failed expression marked rendered")
	}
	if result.Values["data"].(map[string]any)["broken"] != values["data"].(map[string]any)["broken"] {
		t.Fatal("raw expression changed")
	}
}

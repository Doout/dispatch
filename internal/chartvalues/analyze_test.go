package chartvalues

import (
	"reflect"
	"testing"

	"helm.sh/helm/v3/pkg/chart"
)

func TestReferencesAndDynamicImages(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "test"}, Values: map[string]any{"enabled": true}, Templates: []*chart.File{
		{Name: "templates/config.yaml", Data: []byte(`{{ include "image" (dict "root" . "component" .Values.component) }}
{{ tpl .Values.config . }}
{{ index .Values "outside" "key" }}
{{ .Values.notInDefaults }}`)},
		{Name: "templates/_helpers.tpl", Data: []byte(`{{ define "image" }}{{ $images := .root.Values.images }}{{ $entry := get $images .component }}{{ $entry.tag }}{{ end }}`)},
	}}
	values := map[string]any{
		"component": "api", "images": map[string]any{"api": map[string]any{"tag": "v1", "unused": "no"}, "unrelated": map[string]any{"tag": "v2"}},
		"config": `{{ .Values.cluster.host }}`, "cluster": map[string]any{"host": "example", "noise": "no"},
		"outside": map[string]any{"key": "yes", "noise": "no"}, "notInDefaults": "yes", "noise": true,
	}
	before := values["images"].(map[string]any)["unrelated"]
	result, err := Analyze(c, values)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"enabled", "component", "images", "config", "cluster", "outside", "notInDefaults"} {
		if _, ok := result.Values[key]; !ok {
			t.Errorf("missing %s", key)
		}
	}
	if _, ok := result.Values["noise"]; ok {
		t.Fatal("unrelated root retained")
	}
	images := result.Values["images"].(map[string]any)
	if _, ok := images["unrelated"]; ok {
		t.Fatal("unrelated image retained")
	}
	if !reflect.DeepEqual(before, values["images"].(map[string]any)["unrelated"]) {
		t.Fatal("input mutated")
	}
	if _, ok := result.Values["cluster"].(map[string]any)["noise"]; ok {
		t.Fatal("tpl sibling retained")
	}
}
func TestUnknownLookupKeepsSection(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "test"}, Templates: []*chart.File{{Name: "templates/test", Data: []byte(`{{ get .Values.images (include "unknown" .Values.selector) }}`)}}}
	r, err := Analyze(c, map[string]any{"images": map[string]any{"api": "one", "worker": "two"}, "other": "omit"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Values["images"].(map[string]any)) != 2 || len(r.Notes) == 0 {
		t.Fatal(r)
	}
	if _, ok := r.Values["other"]; ok {
		t.Fatal("unrelated section retained")
	}
}
func TestSchemaOpenMapsAndDefaults(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "test"}, Values: map[string]any{"env": map[string]any{}}, Schema: []byte(`{"properties":{"optional":{"type":"string"}},"additionalProperties":true}`)}
	r, err := Analyze(c, map[string]any{"env": map[string]any{"CUSTOM": "yes"}, "optional": "yes", "noise": "no"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Values) != 2 {
		t.Fatal(r)
	}
}
func TestHelperScopeAndRange(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "test"}, Templates: []*chart.File{
		{Name: "templates/test", Data: []byte(`{{ with .Values.worker }}{{ include "image" (dict "root" $ "key" .image) }}{{ end }}{{ range $k,$v := .Values.env }}{{ tpl $v $ }}{{ end }}`)},
		{Name: "templates/_helpers.tpl", Data: []byte(`{{ define "image" }}{{ index .root.Values.images .key "tag" }}{{ end }}`)},
	}}
	r, err := Analyze(c, map[string]any{"worker": map[string]any{"image": "worker"}, "images": map[string]any{"worker": map[string]any{"tag": "v1"}, "noise": "no"}, "env": map[string]any{"HOST": `{{ .Values.host }}`}, "host": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Values["images"].(map[string]any)["noise"]; ok {
		t.Fatal(r)
	}
	if r.Values["host"] != "yes" {
		t.Fatal(r)
	}
}

func TestSchemaReferencesAndLibraryHelpers(t *testing.T) {
	parent := &chart.Chart{Metadata: &chart.Metadata{Name: "parent"}, Schema: []byte(`{"$defs":{"settings":{"properties":{"optional":{"type":"boolean"}}}},"properties":{"settings":{"$ref":"#/$defs/settings"}}}`), Templates: []*chart.File{{Name: "templates/parent", Data: []byte(`{{ include "library.image" . }}`)}}}
	library := &chart.Chart{Metadata: &chart.Metadata{Name: "library", Type: "library"}, Templates: []*chart.File{{Name: "templates/_helper.tpl", Data: []byte(`{{ define "library.image" }}{{ .Values.image.tag }}{{ end }}`)}}}
	parent.AddDependency(library)
	r, err := Analyze(parent, map[string]any{"settings": map[string]any{"optional": true, "noise": false}, "image": map[string]any{"tag": "yes", "noise": "no"}, "other": "no"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Values["settings"].(map[string]any)["optional"] != true {
		t.Fatal(r)
	}
	if _, ok := r.Values["settings"].(map[string]any)["noise"]; ok {
		t.Fatal(r)
	}
	if _, ok := r.Values["other"]; ok {
		t.Fatal(r)
	}
}

func TestUnknownHelperCannotChooseDefaultImage(t *testing.T) {
	c := &chart.Chart{Metadata: &chart.Metadata{Name: "test"}, Templates: []*chart.File{
		{Name: "templates/test", Data: []byte(`{{ $key := default "fallback" (include "selector" .) }}{{ index .Values.images $key }}`)},
		{Name: "templates/_helper.tpl", Data: []byte(`{{ define "selector" }}{{ .Values.selected }}{{ end }}`)},
	}}
	r, err := Analyze(c, map[string]any{"selected": "actual", "images": map[string]any{"actual": "keep", "fallback": "keep"}, "noise": "no"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Values["images"].(map[string]any)) != 2 {
		t.Fatal("guessed helper output")
	}
	if _, ok := r.Values["noise"]; ok {
		t.Fatal("unrelated root")
	}
}

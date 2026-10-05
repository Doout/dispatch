package core

import (
	"testing"
)

func TestPreviewValuesJSONRetainsNumericHelmTypes(t *testing.T) {
	values, err := DecodeWorkflowPreviewValues([]byte(`{"app":{"large":9007199254740993,"unsigned":18446744073709551615,"negative":-9223372036854775808,"fraction":0.125,"nested":[{"count":9007199254740993}],"text":"9007199254740993"}}`))
	if err != nil {
		t.Fatal(err)
	}
	app := values["app"]
	if app["large"] != int64(9007199254740993) || app["unsigned"] != uint64(18446744073709551615) || app["negative"] != int64(-9223372036854775808) || app["fraction"] != float64(0.125) || app["text"] != "9007199254740993" {
		t.Fatal("JSON roundtrip changed numeric values or types", app)
	}
	copy, err := CloneWorkflowPreviewValues(values)
	if err != nil {
		t.Fatal(err)
	}
	nested := copy["app"]["nested"].([]any)[0].(map[string]any)
	if nested["count"] != int64(9007199254740993) {
		t.Fatal("nested integer lost precision", nested)
	}
	nested["count"] = int64(1)
	if values["app"]["nested"].([]any)[0].(map[string]any)["count"] != int64(9007199254740993) {
		t.Fatal("clone mutated the captured values")
	}
	for _, invalid := range []string{`{"app":{"large":18446744073709551616}}`, `{"app":{"negative":-9223372036854775809}}`, `{"app":{"fraction":1e1000}}`, `{"app":{}} {"app":{}}`} {
		if _, err := DecodeWorkflowPreviewValues([]byte(invalid)); err == nil {
			t.Fatal("unsupported numeric or trailing JSON input was accepted")
		}
	}
}

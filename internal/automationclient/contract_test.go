package automationclient

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

// Compare supported routes and typed request properties with the repository's
// OpenAPI document, so a renamed API field cannot silently break automation.
func TestSupportedOpenAPIContract(t *testing.T) {
	raw, e := os.ReadFile("../../docs/openapi.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var spec map[string]any
	if e = yaml.Unmarshal(raw, &spec); e != nil {
		t.Fatal(e)
	}
	paths := spec["paths"].(map[string]any)
	canonical := func(s string) string {
		parts := strings.Split(s, "/")
		for i, p := range parts {
			if strings.HasPrefix(p, "{") {
				parts[i] = "{}"
			}
		}
		return strings.Join(parts, "/")
	}
	for _, op := range Operations {
		found := false
		for path, entry := range paths {
			if canonical(path) == canonical(op.Path) {
				_, found = entry.(map[string]any)[strings.ToLower(op.Method)]
				if found {
					break
				}
			}
		}
		if !found {
			t.Errorf("missing OpenAPI operation %s %s", op.Method, op.Path)
		}
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	values := map[string]any{"DeploymentReview": core.DeploymentReview{ServiceRevisions: map[string]int64{}}, "InfrastructureAcceptance": InfrastructureAcceptance{ReviewID: "review"}, "ServerCreateReview": ServerCreateReview{Bootstrap: &BootstrapInput{}}}
	for name, value := range values {
		schema, ok := schemas[name].(map[string]any)
		if !ok {
			t.Error("missing request schema", name)
			continue
		}
		props := schema["properties"].(map[string]any)
		b, _ := json.Marshal(value)
		var fields map[string]any
		json.Unmarshal(b, &fields)
		actual, expected := []string{}, []string{}
		for k := range fields {
			actual = append(actual, k)
		}
		for k := range props {
			if name == "InfrastructureAcceptance" && k == "requestKey" {
				continue
			}
			expected = append(expected, k)
		}
		sort.Strings(actual)
		sort.Strings(expected)
		if !reflect.DeepEqual(actual, expected) {
			t.Errorf("%s fields changed: %v != %v", name, actual, expected)
		}
	}
}

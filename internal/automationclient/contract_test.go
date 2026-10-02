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
	for _, op := range Operations {
		found := false
		for path, entry := range paths {
			if contractPathMatches(path, op, entry.(map[string]any)) {
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
	values := map[string]any{"DeploymentReview": core.DeploymentReview{ServiceRevisions: map[string]int64{}}, "InfrastructureAcceptance": InfrastructureAcceptance{ReviewID: "review"}, "ServerCreateReview": ServerCreateReview{Bootstrap: &BootstrapInput{}, SourceSnapshotID: "snapshot"}, "ProviderOptionsInput": ProviderOptionsInput{}, "SnapshotReviewInput": SnapshotReviewInput{RetainUntil: "2030-01-01T00:00:00Z"}}
	values["TemporaryEnvironmentInput"] = TemporaryEnvironmentInput{}
	values["TemporaryEnvironmentExtension"] = TemporaryEnvironmentExtension{}
	values["TemporaryEnvironmentCleanup"] = TemporaryEnvironmentCleanup{}
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
			if (name == "ServerCreateReview" || name == "ProviderOptionsInput") && k == "secretRefs" {
				continue // The scoped client deliberately excludes owner-only credentials.
			}
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

// A fixed recovery action must be listed in the path parameter enum; matching
// an arbitrary placeholder would silently admit unsupported action names.
func contractPathMatches(template string, op Operation, entry map[string]any) bool {
	left, right := strings.Split(template, "/"), strings.Split(op.Path, "/")
	if len(left) != len(right) {
		return false
	}
	method, ok := entry[strings.ToLower(op.Method)].(map[string]any)
	if !ok {
		return false
	}
	for i, l := range left {
		if l == right[i] {
			continue
		}
		if !strings.HasPrefix(l, "{") {
			return false
		}
		if strings.HasPrefix(right[i], "{") {
			continue
		}
		name := strings.Trim(l, "{}")
		valid := false
		for _, owner := range []map[string]any{entry, method} {
			params, _ := owner["parameters"].([]any)
			for _, raw := range params {
				param, ok := raw.(map[string]any)
				if !ok || param["in"] != "path" || param["name"] != name {
					continue
				}
				schema, _ := param["schema"].(map[string]any)
				choices, _ := schema["enum"].([]any)
				for _, choice := range choices {
					if choice == right[i] {
						valid = true
					}
				}
			}
		}
		if !valid {
			return false
		}
	}
	return true
}

func TestRecoveryRequestPropertiesMatchOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	var properties func(map[string]any) map[string]any
	properties = func(schema map[string]any) map[string]any {
		out := map[string]any{}
		if ref, ok := schema["$ref"].(string); ok {
			for k, v := range properties(schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)) {
				out[k] = v
			}
		}
		if all, ok := schema["allOf"].([]any); ok {
			for _, part := range all {
				for k, v := range properties(part.(map[string]any)) {
					out[k] = v
				}
			}
		}
		if p, ok := schema["properties"].(map[string]any); ok {
			for k, v := range p {
				out[k] = v
			}
		}
		return out
	}
	values := map[string]any{
		"service_delete":   RecoveryConfirmation{ResourceConfirmation{}},
		"backup_delete":    RecoveryConfirmation{ResourceConfirmation{}},
		"backup_restore":   RecoveryConfirmation{ResourceConfirmation{}},
		"backup_create":    WorkloadBackupCreateInput{Checks: []core.BackupIntegrityCheck{{}}},
		"retention_review": RuntimeRetentionReviewInput{ExpectedPolicy: &recoveryPolicy},
		"retention_apply":  RuntimeRetentionApplyInput{ExpectedPolicy: &recoveryPolicy},
	}
	for name, value := range values {
		op, _ := Find(name)
		var schema map[string]any
		for path, raw := range spec["paths"].(map[string]any) {
			entry := raw.(map[string]any)
			if contractPathMatches(path, op, entry) {
				method := entry[strings.ToLower(op.Method)].(map[string]any)
				schema = method["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
				break
			}
		}
		if schema == nil {
			t.Fatal("missing request schema", name)
		}
		expected := properties(schema)
		actual := map[string]any{}
		json.Unmarshal(recoveryJSON(value), &actual)
		var compare func(string, map[string]any, map[string]any)
		compare = func(path string, want, got map[string]any) {
			expectedKeys, actualKeys := []string{}, []string{}
			for key := range want {
				expectedKeys = append(expectedKeys, key)
			}
			for key := range got {
				actualKeys = append(actualKeys, key)
			}
			sort.Strings(expectedKeys)
			sort.Strings(actualKeys)
			if !reflect.DeepEqual(expectedKeys, actualKeys) {
				t.Errorf("%s contract drift: %v != %v", path, actualKeys, expectedKeys)
				return
			}
			for key, raw := range got {
				if nested, ok := raw.(map[string]any); ok {
					compare(path+"."+key, properties(want[key].(map[string]any)), nested)
				}
			}
		}
		compare(name, expected, actual)
		clientSchema := recoverySchema(op.InputSchema)
		compare(name+" MCP", properties(clientSchema), actual)
	}
	// The registry may only specialize an action that the API explicitly supports.
	op, _ := Find("service_reconcile")
	op.Path = strings.Replace(op.Path, "/reconcile", "/force", 1)
	for path, raw := range spec["paths"].(map[string]any) {
		if contractPathMatches(path, op, raw.(map[string]any)) {
			t.Fatal("an unrestricted action placeholder accepted force")
		}
	}
}

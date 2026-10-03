package automationclient

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
	"github.com/doout/dispatch/internal/provision"
	"gopkg.in/yaml.v3"
)

func TestInfrastructureResponseContracts(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err = yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	var fields func(map[string]any) (map[string]any, []string)
	fields = func(schema map[string]any) (map[string]any, []string) {
		properties, required := map[string]any{}, []string{}
		if ref, ok := schema["$ref"].(string); ok {
			properties, required = fields(schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any))
		}
		if entries, ok := schema["allOf"].([]any); ok {
			for _, entry := range entries {
				part, req := fields(entry.(map[string]any))
				for k, v := range part {
					properties[k] = v
				}
				required = append(required, req...)
			}
		}
		if direct, ok := schema["properties"].(map[string]any); ok {
			for k, v := range direct {
				properties[k] = v
			}
		}
		if direct, ok := schema["required"].([]any); ok {
			for _, v := range direct {
				required = append(required, v.(string))
			}
		}
		return properties, required
	}
	var jsonFields func(reflect.Type) ([]string, []string)
	jsonFields = func(typ reflect.Type) ([]string, []string) {
		properties, required := []string{}, []string{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.Anonymous {
				p, r := jsonFields(field.Type)
				properties, required = append(properties, p...), append(required, r...)
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			properties = append(properties, tag[0])
			if len(tag) == 1 || tag[1] != "omitempty" {
				required = append(required, tag[0])
			}
		}
		return properties, required
	}
	for name, value := range map[string]any{
		"ProjectInfrastructureProvider":              provision.CatalogProvider{},
		"InfrastructureProviderManifest":             provider.Manifest{},
		"InfrastructureProviderSnapshotCapabilities": provider.SnapshotCapabilities{},
		"InfrastructureProviderOption":               provider.Option{},
		"InfrastructureReview":                       core.InfrastructureReview{},
		"ManagedServer":                              core.ManagedServer{},
		"ManagedServerReadiness":                     provision.ServerReadiness{},
		"ManagedServerBootstrapReadiness":            provision.BootstrapReadiness{},
		"InfrastructureOperation":                    core.InfrastructureOperation{},
		"InfrastructureDeletionReview":               provision.DeletionReview{},
		"InfrastructureAccepted":                     provision.Accepted{},
	} {
		properties, required := fields(schemas[name].(map[string]any))
		actual := []string{}
		for key := range properties {
			actual = append(actual, key)
		}
		expected, requiredExpected := jsonFields(reflect.TypeOf(value))
		sort.Strings(actual)
		sort.Strings(expected)
		sort.Strings(required)
		sort.Strings(requiredExpected)
		if !reflect.DeepEqual(actual, expected) || !reflect.DeepEqual(required, requiredExpected) {
			t.Errorf("%s response contract changed: fields %v != %v; required %v != %v", name, actual, expected, required, requiredExpected)
		}
	}
	paths := spec["paths"].(map[string]any)
	for _, tc := range []struct {
		path, method, status, schema string
		array                        bool
	}{
		{"/projects/{id}/infrastructure/providers", "get", "200", "ProjectInfrastructureProvider", true},
		{"/projects/{id}/infrastructure/providers/{providerId}/options", "post", "200", "InfrastructureProviderOption", true},
		{"/infrastructure/servers", "get", "200", "ManagedServer", true},
		{"/infrastructure/servers/{id}", "get", "200", "ManagedServerReadiness", false},
		{"/infrastructure/servers/{id}/operations", "get", "200", "InfrastructureOperation", true},
		{"/infrastructure/servers/review", "post", "201", "InfrastructureReview", false},
		{"/infrastructure/servers/{id}/delete-review", "post", "200", "InfrastructureDeletionReview", false},
		{"/infrastructure/servers/{id}/adopt", "post", "200", "ManagedServer", false},
	} {
		response := paths[tc.path].(map[string]any)[tc.method].(map[string]any)["responses"].(map[string]any)[tc.status].(map[string]any)
		schema := response["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if tc.array {
			if schema["type"] != "array" {
				t.Fatal("inventory schema is not an array", tc.path)
			}
			schema = schema["items"].(map[string]any)
		}
		if schema["$ref"] != "#/components/schemas/"+tc.schema {
			t.Fatal("wrong response schema", tc.path, schema)
		}
	}
	backup, required := fields(schemas["WorkloadBackup"].(map[string]any))
	if backup["capturePolicyId"].(map[string]any)["type"] != "string" || backup["scheduledAt"].(map[string]any)["format"] != "date-time" {
		t.Fatal("scheduled capture provenance lost its types")
	}
	for _, name := range required {
		if name == "capturePolicyId" || name == "scheduledAt" {
			t.Fatal("manual captures require schedule provenance")
		}
	}
}

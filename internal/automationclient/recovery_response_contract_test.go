package automationclient

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

// Compare nested public JSON, including fields not yet used by the browser.
// This also prevents encrypted recovery inputs from becoming public fields.
func TestRecoveryResponseContracts(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)
	for name, value := range map[string]any{
		"WorkloadBackup":            core.WorkloadBackup{},
		"WorkloadBackupOperation":   core.WorkloadBackupOperation{},
		"WorkloadBackupPolicy":      core.WorkloadBackupPolicy{},
		"BackupObjectStore":         core.BackupObjectStore{},
		"BackupOffsiteArtifact":     core.BackupOffsiteArtifact{},
		"RuntimeRetentionReview":    core.RuntimeRetentionReview{},
		"ServiceResource":           core.ServiceResource{},
		"ServiceResourceInspection": core.ServiceResourceInspection{},
		"RetentionResult":           core.RetentionResult{},
	} {
		t.Run(name, func(t *testing.T) {
			compareResponseSchema(t, name, reflect.TypeOf(value), schemas[name].(map[string]any), schemas)
		})
	}
}

func compareResponseSchema(t *testing.T, path string, typ reflect.Type, schema map[string]any, schemas map[string]any) {
	t.Helper()
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if ref, ok := schema["$ref"].(string); ok {
		schema = schemas[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any)
	}
	matches := func(expected string) bool {
		if kinds, ok := schema["type"].([]any); ok {
			for _, kind := range kinds {
				if kind == expected {
					return true
				}
			}
			return false
		}
		return schema["type"] == expected
	}
	wantType := ""
	switch typ.Kind() {
	case reflect.String:
		wantType = "string"
	case reflect.Bool:
		wantType = "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		wantType = "integer"
	case reflect.Float32, reflect.Float64:
		wantType = "number"
	case reflect.Slice, reflect.Array:
		wantType = "array"
	case reflect.Struct, reflect.Map:
		wantType = "object"
	default:
		t.Fatalf("%s: unsupported JSON type %s", path, typ)
	}
	if typ == reflect.TypeOf(time.Time{}) {
		if !matches("string") || schema["format"] != "date-time" {
			t.Errorf("%s: timestamp schema changed: %v", path, schema)
		}
		return
	}
	if !matches(wantType) {
		t.Errorf("%s: %s requires %s schema, got %v", path, typ, wantType, schema["type"])
		return
	}
	if wantType == "array" {
		compareResponseSchema(t, path+"[]", typ.Elem(), schema["items"].(map[string]any), schemas)
		return
	}
	if wantType != "object" {
		return
	}
	if typ.Kind() == reflect.Map {
		if values, ok := schema["additionalProperties"].(map[string]any); ok {
			compareResponseSchema(t, path+".*", typ.Elem(), values, schemas)
		}
		return
	}
	properties := schema["properties"].(map[string]any)
	fields, optional := map[string]reflect.Type{}, map[string]bool{}
	var collect func(reflect.Type)
	collect = func(owner reflect.Type) {
		for i := 0; i < owner.NumField(); i++ {
			field := owner.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			if tag[0] == "-" {
				continue
			}
			if field.Anonymous && tag[0] == "" {
				collect(field.Type)
				continue
			}
			name := tag[0]
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
			for _, option := range tag[1:] {
				if option == "omitempty" {
					optional[name] = true
				}
			}
		}
	}
	collect(typ)
	for name, field := range fields {
		documented, ok := properties[name].(map[string]any)
		if !ok {
			t.Errorf("%s.%s: public field missing from OpenAPI", path, name)
			continue
		}
		compareResponseSchema(t, path+"."+name, field, documented, schemas)
	}
	for name := range properties {
		if _, ok := fields[name]; !ok {
			t.Errorf("%s.%s: OpenAPI field absent from public JSON", path, name)
		}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, entry := range required {
			name := entry.(string)
			if optional[name] {
				t.Errorf("%s.%s: required OpenAPI field can be omitted", path, name)
			}
		}
	}
}

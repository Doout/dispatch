package automationclient

import (
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

func TestCoreCompletionScopedInputContracts(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	for name, input := range map[string]any{
		"BootstrapRetryInput":    BootstrapRetryInput{},
		"ServerPowerInput":       ServerPowerInput{},
		"ClonePromotionInput":    ClonePromotionInput{},
		"ApplicationUpdateInput": ApplicationUpdateInput{},
	} {
		t.Run(name, func(t *testing.T) {
			apiSchema := schemas[name].(map[string]any)
			if name == "ApplicationUpdateInput" {
				// The API also accepts owner-only credential changes. Scoped
				// discovery and request parsing deliberately exclude those fields.
				copy := map[string]any{}
				for key, value := range apiSchema {
					copy[key] = value
				}
				properties := map[string]any{}
				for key, value := range apiSchema["properties"].(map[string]any) {
					if key != "sourceAuthType" && key != "sourceCredentialId" {
						properties[key] = value
					}
				}
				copy["properties"] = properties
				apiSchema = copy
			}
			mcpSchema := inputSchema(name).(map[string]any)
			compareResponseSchema(t, name+" API", reflect.TypeOf(input), apiSchema, schemas)
			compareResponseSchema(t, name+" MCP", reflect.TypeOf(input), mcpSchema, schemas)
			compareScopedInputConstraints(t, name, apiSchema, mcpSchema)
		})
	}
	// Inspect/retry expose the full nested public receipt and pinned plan, while
	// ClaimHash and EncryptedInput must remain absent from both public schemas.
	compareResponseSchema(t, "TargetBootstrap", reflect.TypeOf(core.TargetBootstrap{}), schemas["TargetBootstrap"].(map[string]any), schemas)
}

func compareScopedInputConstraints(t *testing.T, path string, apiSchema, mcpSchema map[string]any) {
	t.Helper()
	for _, key := range []string{"additionalProperties", "required", "minProperties", "minLength", "maxLength", "minimum", "maximum", "pattern", "enum"} {
		apiValue, mcpValue := apiSchema[key], mcpSchema[key]
		if key == "required" || key == "enum" {
			apiValue, mcpValue = normalizedSchemaChoices(apiValue), normalizedSchemaChoices(mcpValue)
		}
		if key == "minimum" || key == "maximum" || key == "minLength" || key == "maxLength" || key == "minProperties" {
			if number, ok := apiValue.(int); ok {
				apiValue = int64(number)
			}
			if number, ok := mcpValue.(int); ok {
				mcpValue = int64(number)
			}
		}
		if !reflect.DeepEqual(apiValue, mcpValue) {
			t.Errorf("%s %s differs between API and MCP: %v != %v", path, key, apiValue, mcpValue)
		}
	}
	if apiProperties, ok := apiSchema["properties"].(map[string]any); ok {
		mcpProperties := mcpSchema["properties"].(map[string]any)
		for field, property := range apiProperties {
			if counterpart, ok := mcpProperties[field].(map[string]any); ok {
				compareScopedInputConstraints(t, path+"."+field, property.(map[string]any), counterpart)
			}
		}
	}
}

func normalizedSchemaChoices(value any) []string {
	choices := []string{}
	switch values := value.(type) {
	case []string:
		choices = append(choices, values...)
	case []any:
		for _, value := range values {
			choices = append(choices, value.(string))
		}
	}
	sort.Strings(choices)
	return choices
}

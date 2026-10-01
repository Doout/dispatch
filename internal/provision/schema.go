package provision

import (
	"encoding/json"
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

type noSchemaDownloads struct{}

func (noSchemaDownloads) Load(string) (any, error) {
	return nil, errors.New("external schema references are not supported")
}

func configurationSchema(raw json.RawMessage) (*jsonschema.Schema, map[string]json.RawMessage, error) {
	var object map[string]any
	if len(raw) > 128<<10 || json.Unmarshal(raw, &object) != nil || object["type"] != "object" {
		return nil, nil, errors.New("provider configuration schema must be a bounded object schema")
	}
	var fields struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return nil, nil, errors.New("provider configuration properties are invalid")
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(noSchemaDownloads{})
	if err := compiler.AddResource("https://dispatch.invalid/provider-configuration.json", object); err != nil {
		return nil, nil, errors.New("provider configuration schema is invalid")
	}
	schema, err := compiler.Compile("https://dispatch.invalid/provider-configuration.json")
	if err != nil {
		return nil, nil, errors.New("provider configuration schema is invalid or refers to an external document")
	}
	return schema, fields.Properties, nil
}

// Configuration rejects undeclared top-level fields even when a provider's
// schema allows extras. Schema errors must not expose submitted secret values.
func ValidateConfiguration(raw json.RawMessage, config map[string]any) error {
	schema, properties, err := configurationSchema(raw)
	if err != nil {
		return err
	}
	if config == nil {
		config = map[string]any{}
	}
	for field := range config {
		if _, ok := properties[field]; !ok {
			return errors.New("configuration contains a field absent from the provider schema")
		}
	}
	if schema.Validate(config) != nil {
		return errors.New("configuration does not satisfy the provider schema")
	}
	return nil
}

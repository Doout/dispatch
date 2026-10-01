package provision

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/provider"
)

func (m *Manager) resolveConfiguration(ctx context.Context, schema json.RawMessage, values map[string]any, refs map[string]string) (map[string]any, []string, error) {
	_, fields, err := configurationSchema(schema)
	if err != nil {
		return nil, nil, err
	}
	config := map[string]any{}
	secrets := []string{}
	for k, v := range values {
		var field struct {
			WriteOnly bool `json:"writeOnly"`
		}
		raw, ok := fields[k]
		if !ok {
			return nil, nil, errors.New("configuration contains an unknown field")
		}
		if json.Unmarshal(raw, &field) != nil || field.WriteOnly {
			return nil, nil, errors.New("write-only fields require secret references")
		}
		config[k] = v
	}
	for k, id := range refs {
		if _, ok := fields[k]; !ok {
			return nil, nil, errors.New("secret reference names an unknown field")
		}
		if _, ok := values[k]; ok {
			return nil, nil, errors.New("choose a value or secret reference for each field")
		}
		secret, e := m.Store.GetSecret(ctx, id)
		if e != nil || core.PlainSecretType(secret.Type) || core.JSONSecretType(secret.Type) || secret.Type == core.SecretTypeSSHPrivateKey {
			return nil, nil, errors.New("provider fields require write-only text secrets")
		}
		if m.Secrets == nil {
			return nil, nil, errors.New("secret resolver is unavailable")
		}
		value, e := m.Secrets.Resolve(ctx, id)
		if e != nil {
			return nil, nil, errors.New("provider field secret is unavailable")
		}
		config[k] = string(value)
		secrets = append(secrets, string(value))
	}
	if err = ValidateConfiguration(schema, config); err != nil {
		return nil, nil, err
	}
	return config, secrets, nil
}
func containsSecret(value any, secrets []string) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return true
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return true
	}
	var visit func(any) bool
	visit = func(v any) bool {
		switch item := v.(type) {
		case string:
			for _, secret := range secrets {
				if secret != "" && strings.Contains(item, secret) {
					return true
				}
			}
		case []any:
			for _, child := range item {
				if visit(child) {
					return true
				}
			}
		case map[string]any:
			for k, child := range item {
				if visit(k) || visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(decoded)
}
func (m *Manager) safeEvidence(r core.InfrastructureReview, value any) bool {
	var input CreateInput
	if json.Unmarshal(r.Input, &input) != nil {
		return false
	}
	if len(input.SecretRefs) == 0 {
		return true
	}
	saved, err := m.reviewedRequest(r)
	if err != nil {
		return false
	}
	secrets := []string{}
	for field := range input.SecretRefs {
		if value, ok := saved.ProviderConfig[field].(string); ok {
			secrets = append(secrets, value)
		}
	}
	return !containsSecret(value, secrets)
}

type ProjectOptionInput struct {
	Kind       string            `json:"kind"`
	Config     map[string]any    `json:"config"`
	SecretRefs map[string]string `json:"secretRefs,omitempty"`
}

func (m *Manager) ProjectOptions(ctx context.Context, project, id string, in ProjectOptionInput) ([]provider.Option, error) {
	if err := m.authorize(ctx, project, id, "infrastructure.inspect"); err != nil {
		return nil, err
	}
	client, p, err := m.Adapter(ctx, id, provider.CapabilityInspect, "")
	if err != nil {
		return nil, err
	}
	var manifest provider.Manifest
	if json.Unmarshal(p.Manifest, &manifest) != nil {
		return nil, ErrIdentity
	}
	config, secrets, err := m.resolveConfiguration(ctx, manifest.ConfigurationSchema, in.Config, in.SecretRefs)
	if err != nil {
		return nil, err
	}
	if err = client.Validate(ctx, config); err != nil {
		return nil, errors.New("provider rejected configuration")
	}
	items, err := client.Options(ctx, provider.OptionRequest{Kind: in.Kind, Config: config})
	if err != nil || containsSecret(items, secrets) {
		return nil, errors.New("provider options are unavailable or contain private configuration")
	}
	return items, nil
}

package provider

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

const (
	CapabilityCreate    = "server.create"
	CapabilityInspect   = "server.inspect"
	CapabilityDelete    = "server.delete"
	CapabilityOwnership = "server.ownership"
	StatePending        = "pending"
	StateRunning        = "running"
	StateSucceeded      = "succeeded"
	StateFailed         = "failed"
	StateCancelled      = "cancelled"
)

var ErrAPIVersion = errors.New("unsupported provider API version; supported versions: " + APIVersion)

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$`)

func ValidID(value string) bool { return identifier.MatchString(value) }

func ValidateManifest(manifest Manifest) error {
	if manifest.APIVersion != APIVersion {
		return ErrAPIVersion
	}
	if !ValidID(manifest.Name) || strings.TrimSpace(manifest.DisplayName) == "" || strings.TrimSpace(manifest.Version) == "" {
		return errors.New("provider manifest identity is incomplete")
	}
	seen := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		if !ValidID(capability) || seen[capability] {
			return errors.New("provider capabilities are invalid or duplicated")
		}
		seen[capability] = true
	}
	if len(seen) == 0 {
		return errors.New("provider manifest has no capabilities")
	}
	if err := validateServerActionCapabilities(seen); err != nil {
		return err
	}
	if err := validateSnapshotCapabilities(manifest, seen); err != nil {
		return err
	}
	var schema map[string]json.RawMessage
	if json.Unmarshal(manifest.ConfigurationSchema, &schema) != nil || schema == nil {
		return errors.New("provider configuration schema must be a JSON object")
	}
	var kind string
	if json.Unmarshal(schema["type"], &kind) != nil || kind != "object" {
		return errors.New("provider configuration schema must describe an object")
	}
	if raw, ok := schema["properties"]; ok {
		var properties map[string]json.RawMessage
		if json.Unmarshal(raw, &properties) != nil || properties == nil {
			return errors.New("provider configuration schema properties are invalid")
		}
	}
	if raw, ok := schema["required"]; ok {
		var required []string
		if json.Unmarshal(raw, &required) != nil || required == nil {
			return errors.New("provider configuration schema required fields are invalid")
		}
		seen := map[string]bool{}
		for _, name := range required {
			if name == "" || seen[name] {
				return errors.New("provider configuration schema required fields are duplicated or empty")
			}
			seen[name] = true
		}
	}
	return nil
}

func ValidateOperation(operation Operation) error {
	if !ValidID(operation.ID) {
		return errors.New("provider operation has an invalid identity")
	}
	switch operation.State {
	case StatePending, StateRunning, StateSucceeded, StateFailed, StateCancelled:
	default:
		return errors.New("provider operation has an unknown state")
	}
	if operation.ResourceID != "" && !ValidID(operation.ResourceID) || operation.State == StateSucceeded && operation.ResourceID == "" {
		return errors.New("provider operation has an invalid resource identity")
	}
	return nil
}

func Terminal(operation Operation) bool {
	return operation.State == StateSucceeded || operation.State == StateFailed || operation.State == StateCancelled
}

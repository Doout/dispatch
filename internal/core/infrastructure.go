package core

import (
	"encoding/json"
	"time"
)

// InfrastructureProvider keeps credentials in the existing write-only secret
// store. The endpoint and route are pinned when the registration is created.
type InfrastructureProvider struct {
	ID                 string          `json:"id"`
	Name               string          `json:"name"`
	Endpoint           string          `json:"endpoint"`
	PrivateNetworkID   string          `json:"privateNetworkId,omitempty"`
	CredentialSecretID string          `json:"credentialSecretId,omitempty"`
	Enabled            bool            `json:"enabled"`
	Capabilities       []string        `json:"capabilities"`
	Manifest           json.RawMessage `json:"manifest,omitempty"`
	ManifestDigest     string          `json:"manifestDigest,omitempty"`
	State              string          `json:"state"`
	LastError          string          `json:"lastError,omitempty"`
	Revision           int64           `json:"revision"`
	LastVerifiedAt     *time.Time      `json:"lastVerifiedAt,omitempty"`
	CreatedAt          time.Time       `json:"createdAt"`
	UpdatedAt          time.Time       `json:"updatedAt"`
}

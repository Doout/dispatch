package core

import "time"

const (
	AuthProviderGitHub = "github"

	AuthProviderStateReady    = "ready"
	AuthProviderStateDisabled = "disabled"

	AuthProvisionExisting = "existing"
	AuthProvisionApproval = "approval"
)

type AuthProvider struct {
	ID                     string     `json:"id"`
	Name                   string     `json:"name"`
	Type                   string     `json:"type"`
	BaseURL                string     `json:"baseUrl"`
	APIURL                 string     `json:"apiUrl"`
	ClientID               string     `json:"clientId"`
	EncryptedClientSecret  string     `json:"-"`
	ClientSecretConfigured bool       `json:"clientSecretConfigured"`
	Provisioning           string     `json:"provisioning"`
	State                  string     `json:"state"`
	LastVerifiedAt         *time.Time `json:"lastVerifiedAt,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
}

type ExternalIdentity struct {
	ProviderID string    `json:"providerId"`
	Subject    string    `json:"subject"`
	UserID     string    `json:"userId"`
	Login      string    `json:"login"`
	Email      string    `json:"email,omitempty"`
	LastLogin  time.Time `json:"lastLogin"`
	CreatedAt  time.Time `json:"createdAt"`
}

package core

import "time"

// LanewayApplication is one reusable client registration on a Laneway authority.
// Its client secret is encrypted and is never returned by the API.
type LanewayApplication struct {
	ID                    string    `json:"id"`
	Name                  string    `json:"name"`
	Authority             string    `json:"authority"`
	RemoteApplicationID   string    `json:"applicationId"`
	ClientID              string    `json:"clientId"`
	EncryptedClientSecret string    `json:"-"`
	State                 string    `json:"state"`
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// LanewayAuthorizationTransaction holds one browser registration or network
// installation handshake. StateHash is a hash of the browser-visible state.
type LanewayAuthorizationTransaction struct {
	StateHash             string
	Kind                  string
	ConnectionName        string
	Authority             string
	RedirectURI           string
	ApplicationID         string
	EncryptedCodeVerifier string
	InitiatingUserID      string
	ExpiresAt             time.Time
	ConsumedAt            *time.Time
	CreatedAt             time.Time
}

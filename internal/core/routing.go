package core

import "time"

// RoutingConfig describes the target's dedicated Traefik routing zone. The
// writable provider directory is operator configuration, never API input.
type RoutingConfig struct {
	BaseDomain  string `json:"baseDomain"`
	EntryPoint  string `json:"entryPoint"`
	TLSResolver string `json:"tlsResolver,omitempty"`
	RequireTLS  bool   `json:"requireTls"`
	// ComposeService selects the sole HTTP ingress service for Compose apps.
	ComposeService string `json:"composeService,omitempty"`
}

type RouteCertificate struct {
	State     string     `json:"state"`
	Message   string     `json:"message"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
}

// ApplicationRoute distinguishes a prepared or published destination from a
// public endpoint whose DNS and certificate have actually been verified.
type ApplicationRoute struct {
	AppID                 string           `json:"appId"`
	ProjectID             string           `json:"projectId"`
	ServerID              string           `json:"serverId"`
	Hostname              string           `json:"hostname"`
	EntryPoint            string           `json:"entryPoint"`
	TLSResolver           string           `json:"tlsResolver,omitempty"`
	RequireTLS            bool             `json:"requireTls"`
	RequestedDeploymentID string           `json:"requestedDeploymentId"`
	DeploymentID          string           `json:"deploymentId,omitempty"`
	Destination           string           `json:"destination,omitempty"`
	PreviousDeploymentID  string           `json:"previousDeploymentId,omitempty"`
	PreviousDestination   string           `json:"previousDestination,omitempty"`
	State                 string           `json:"state"`
	Message               string           `json:"message"`
	DNS                   string           `json:"dns"`
	Certificate           RouteCertificate `json:"certificate"`
	PublishedAt           *time.Time       `json:"publishedAt,omitempty"`
	UpdatedAt             time.Time        `json:"updatedAt"`
}

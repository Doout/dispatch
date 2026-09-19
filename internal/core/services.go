package core

import "time"

// Service is a connection registration. Dispatch never owns the remote resource.
type Service struct {
	ID          string                  `json:"id"`
	ProjectID   string                  `json:"projectId"`
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Type        string                  `json:"type"`
	Fields      map[string]ServiceField `json:"fields"`
	ProbeHost   string                  `json:"probeHost,omitempty"`
	ProbePort   int                     `json:"probePort,omitempty"`
	Revision    int64                   `json:"revision"`
	Check       *ServiceCheck           `json:"check,omitempty"`
	CreatedAt   time.Time               `json:"createdAt"`
	UpdatedAt   time.Time               `json:"updatedAt"`
}

type ServiceField struct {
	Value               string `json:"value,omitempty"`
	Sensitive           bool   `json:"sensitive"`
	Configured          bool   `json:"configured"`
	SecretRef           string `json:"secretRef,omitempty"`
	EncryptedValue      string `json:"-"`
	CapturedSecretID    string `json:"-"`
	CapturedSecretValue string `json:"-"`
}

type ServiceCheck struct {
	State      string    `json:"state"`
	Message    string    `json:"message"`
	Location   string    `json:"location"`
	CheckedAt  time.Time `json:"checkedAt"`
	DurationMS int64     `json:"durationMs"`
}

// Maps are destination -> source field. Helm destinations receive only Secret
// names or key names, never the credential itself.
type ServiceBinding struct {
	Alias       string                       `json:"alias" yaml:"alias"`
	ServiceRef  string                       `json:"serviceRef" yaml:"serviceRef"`
	Environment map[string]string            `json:"environment,omitempty" yaml:"environment,omitempty"`
	Compose     map[string]map[string]string `json:"compose,omitempty" yaml:"compose,omitempty"`
	Helm        *ServiceHelmBinding          `json:"helm,omitempty" yaml:"helm,omitempty"`
}
type ServiceHelmBinding struct {
	Keys             map[string]string `json:"keys" yaml:"keys"`
	SecretNameValues []string          `json:"secretNameValues" yaml:"secretNameValues"`
	KeyValues        map[string]string `json:"keyValues,omitempty" yaml:"keyValues,omitempty"`
}
type CapturedServiceBinding struct {
	Binding ServiceBinding
	Service Service
}
type AppliedServiceBinding struct {
	Alias       string `json:"alias"`
	ServiceID   string `json:"serviceId"`
	ServiceName string `json:"serviceName"`
	Revision    int64  `json:"revision"`
}
type ServiceConsumer struct {
	AppID                string `json:"appId"`
	AppName              string `json:"appName"`
	Alias                string `json:"alias"`
	AppliedRevision      int64  `json:"appliedRevision"`
	RedeploymentRequired bool   `json:"redeploymentRequired"`
}
type ServiceRuntimeBinding struct {
	SensitiveValues []string
	Binding         ServiceBinding
	Values          map[string]string
}

type ServiceOverview struct {
	Service
	Consumers       []ServiceConsumer `json:"consumers"`
	AvailableFields []string          `json:"availableFields"`
}

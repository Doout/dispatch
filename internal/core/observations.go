package core

import "time"

// ObservationConfig belongs to the application's project. Delivery URLs can contain
// credentials and are encrypted separately; they are never returned to clients.
type ObservationConfig struct {
	AppID                string     `json:"appId"`
	ProjectID            string     `json:"projectId"`
	Revision             int64      `json:"revision"`
	Scheduled            bool       `json:"scheduled"`
	IntervalSeconds      int        `json:"intervalSeconds"`
	StaleAfterSeconds    int        `json:"staleAfterSeconds"`
	EndpointURL          string     `json:"endpointUrl,omitempty"`
	NotificationsEnabled bool       `json:"notificationsEnabled"`
	WebhookConfigured    bool       `json:"webhookConfigured"`
	MutedUntil           *time.Time `json:"mutedUntil,omitempty"`
	UpdatedAt            time.Time  `json:"updatedAt"`
	UpdatedBy            string     `json:"updatedBy,omitempty"`
	WebhookCiphertext    string     `json:"-"`
}

type EndpointObservation struct {
	State                string     `json:"state"`
	TLS                  string     `json:"tls"`
	HTTPStatus           int        `json:"httpStatus,omitempty"`
	CertificateExpiresAt *time.Time `json:"certificateExpiresAt,omitempty"`
	DurationMS           int64      `json:"durationMs"`
	Message              string     `json:"message"`
	Location             string     `json:"location"`
}

type ApplicationObservation struct {
	AppID                 string              `json:"appId"`
	ProjectID             string              `json:"projectId"`
	ConfigurationRevision int64               `json:"configurationRevision"`
	DeploymentID          string              `json:"deploymentId,omitempty"`
	Source                string              `json:"source"`
	State                 string              `json:"state"`
	Drift                 string              `json:"drift"`
	Health                string              `json:"health"`
	Endpoint              EndpointObservation `json:"endpoint"`
	CheckedAt             *time.Time          `json:"checkedAt,omitempty"`
	NextCheckAt           *time.Time          `json:"nextCheckAt,omitempty"`
	ConsecutiveFailures   int                 `json:"consecutiveFailures"`
	Message               string              `json:"message,omitempty"`
	Location              string              `json:"location"`
}

// ObservationEvent is a sanitized transition and persistent delivery outbox item.
// Repeated observations in the same state do not create new notifications.
type ObservationEvent struct {
	ID                    string     `json:"id"`
	AppID                 string     `json:"appId"`
	ProjectID             string     `json:"projectId"`
	ConfigurationRevision int64      `json:"configurationRevision"`
	DeploymentID          string     `json:"deploymentId,omitempty"`
	Kind                  string     `json:"kind"`
	PreviousState         string     `json:"previousState,omitempty"`
	State                 string     `json:"state"`
	Message               string     `json:"message"`
	Link                  string     `json:"link"`
	CreatedAt             time.Time  `json:"createdAt"`
	Delivery              string     `json:"delivery"`
	Attempts              int        `json:"attempts"`
	NextAttemptAt         *time.Time `json:"nextAttemptAt,omitempty"`
	DeliveredAt           *time.Time `json:"deliveredAt,omitempty"`
	DeliveryMessage       string     `json:"deliveryMessage,omitempty"`
}

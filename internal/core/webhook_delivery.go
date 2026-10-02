package core

import "time"

// WebhookDelivery is a durable receipt. Ciphertext and lease tokens never leave the controller.
type WebhookDelivery struct {
	ID             string          `json:"id"`
	ConnectionID   string          `json:"connectionId"`
	DeliveryID     string          `json:"deliveryId"`
	Event          string          `json:"event"`
	Repository     string          `json:"repository,omitempty"`
	InstallationID int64           `json:"installationId,omitempty"`
	State          string          `json:"state"`
	Attempts       int             `json:"attempts"`
	Error          string          `json:"error,omitempty"`
	ReceivedAt     time.Time       `json:"receivedAt"`
	ExpiresAt      time.Time       `json:"expiresAt"`
	NextAttemptAt  time.Time       `json:"nextAttemptAt,omitempty"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
	Result         string          `json:"result,omitempty"`
	BodyDigest     string          `json:"-"`
	Ciphertext     string          `json:"-"`
	LeaseToken     string          `json:"-"`
	LeaseUntil     time.Time       `json:"-"`
	Activities     []EventActivity `json:"-"`
}

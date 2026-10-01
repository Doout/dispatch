package core

import (
	"encoding/json"
	"time"
)

// InfrastructureSnapshot records retained provider storage independently from
// source machine and clone lifetimes. Unknown captures remain owned and counted.
type InfrastructureSnapshot struct {
	ID          string          `json:"id"`
	ReviewID    string          `json:"reviewId"`
	ServerID    string          `json:"serverId"`
	ProjectID   string          `json:"projectId"`
	ProviderID  string          `json:"providerId"`
	Name        string          `json:"name"`
	ResourceID  string          `json:"resourceId,omitempty"`
	State       string          `json:"state"`
	Evidence    json.RawMessage `json:"evidence,omitempty"`
	RetainUntil time.Time       `json:"retainUntil"`
	Revision    int64           `json:"revision"`
	CreatedAt   time.Time       `json:"createdAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
}

type InfrastructureSnapshotReview struct {
	ID               string          `json:"id"`
	SnapshotID       string          `json:"snapshotId"`
	ServerID         string          `json:"serverId"`
	ProjectID        string          `json:"projectId"`
	ProviderID       string          `json:"providerId"`
	ProviderRevision int64           `json:"providerRevision"`
	ManifestDigest   string          `json:"manifestDigest"`
	Name             string          `json:"name"`
	Action           string          `json:"action"`
	Input            json.RawMessage `json:"input"`
	EncryptedRequest string          `json:"-"`
	Digest           string          `json:"digest"`
	State            string          `json:"state"`
	OperationID      string          `json:"operationId,omitempty"`
	RetainUntil      time.Time       `json:"retainUntil"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	CreatedAt        time.Time       `json:"createdAt"`
}

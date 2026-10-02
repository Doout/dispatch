package core

import (
	"encoding/json"
	"time"
)

type InfrastructureAcceptance struct {
	ActorID       string
	ProjectID     string
	ProviderID    string
	OperationID   string
	ServerID      string
	Action        string
	ResourceID    string
	DesiredConfig json.RawMessage
}

type InfrastructureReview struct {
	BootstrapID      string          `json:"bootstrapId,omitempty"`
	ID               string          `json:"id"`
	ServerID         string          `json:"serverId"`
	ProjectID        string          `json:"projectId"`
	ProviderID       string          `json:"providerId"`
	ProviderRevision int64           `json:"providerRevision"`
	ManifestDigest   string          `json:"manifestDigest"`
	Name             string          `json:"name"`
	Input            json.RawMessage `json:"input"`
	EncryptedRequest string          `json:"-"`
	Digest           string          `json:"digest"`
	State            string          `json:"state"`
	ExpiresAt        time.Time       `json:"expiresAt"`
	CreatedAt        time.Time       `json:"createdAt"`
}

type ManagedServer struct {
	BootstrapID     string    `json:"bootstrapId,omitempty"`
	ID              string    `json:"id"`
	ReviewID        string    `json:"reviewId"`
	ProjectID       string    `json:"projectId"`
	ProviderID      string    `json:"providerId"`
	Name            string    `json:"name"`
	NodeID          string    `json:"nodeId"`
	ResourceID      string    `json:"resourceId,omitempty"`
	Address         string    `json:"address,omitempty"`
	AllocationState string    `json:"allocationState"`
	EnrollmentState string    `json:"enrollmentState"`
	RuntimeState    string    `json:"runtimeState"`
	Revision        int64     `json:"revision"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type InfrastructureOperation struct {
	ID                  string    `json:"id"`
	ServerID            string    `json:"serverId"`
	ProviderID          string    `json:"providerId"`
	ActorID             string    `json:"actorId"`
	Action              string    `json:"action"`
	State               string    `json:"state"`
	Stage               string    `json:"stage"`
	ProviderOperationID string    `json:"providerOperationId,omitempty"`
	ResourceID          string    `json:"resourceId,omitempty"`
	ErrorCode           string    `json:"errorCode,omitempty"`
	Message             string    `json:"message,omitempty"`
	Attempts            int       `json:"attempts"`
	CancelRequested     bool      `json:"cancelRequested"`
	ExpiresAt           time.Time `json:"expiresAt"`
	NextAttemptAt       time.Time `json:"nextAttemptAt"`
	LeaseToken          string    `json:"-"`
	LeaseUntil          time.Time `json:"-"`
	RequestDigest       string    `json:"-"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

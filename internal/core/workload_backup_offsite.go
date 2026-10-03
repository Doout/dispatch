package core

import (
	"github.com/doout/dispatch/internal/backupstore"
	"time"
)

// BackupObjectStore fixes one project's destination and references a write-only credential.
type BackupObjectStore struct {
	ID                 string             `json:"id"`
	ProjectID          string             `json:"projectId"`
	Name               string             `json:"name"`
	Config             backupstore.Config `json:"config"`
	CredentialSecretID string             `json:"credentialSecretId"`
	CreatedAt          time.Time          `json:"createdAt"`
}
type BackupOffsiteArtifact struct {
	VerifiedAt        *time.Time `json:"verifiedAt,omitempty"`
	VerificationState string     `json:"verificationState,omitempty"`
	DeletedAt         *time.Time `json:"deletedAt,omitempty"`
	StoreID           string     `json:"storeId"`
	ArchiveKey        string     `json:"archiveKey"`
	ManifestKey       string     `json:"manifestKey"`
	ManifestChecksum  string     `json:"manifestChecksum"`
	ImageReference    string     `json:"imageReference"`
	ConfirmedAt       time.Time  `json:"confirmedAt"`
}

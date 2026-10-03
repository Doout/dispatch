package core

import "time"
import "github.com/doout/dispatch/internal/backupstore"

type WorkloadBackup struct {
	LocalState                string                 `json:"localState,omitempty"`
	ID                        string                 `json:"id"`
	Offsite                   *BackupOffsiteArtifact `json:"offsite,omitempty"`
	CapturePolicyID           string                 `json:"capturePolicyId,omitempty"`
	ScheduledAt               *time.Time             `json:"scheduledAt,omitempty"`
	ProjectID                 string                 `json:"projectId"`
	SourceRunID               string                 `json:"sourceRunId"`
	StorageID                 string                 `json:"storageId"`
	ServerID                  string                 `json:"serverId"`
	NodeID                    string                 `json:"nodeId,omitempty"`
	SourceResourceID          string                 `json:"sourceResourceId"`
	State                     string                 `json:"state"`
	Revision                  int64                  `json:"revision"`
	ArtifactID                string                 `json:"artifactId"`
	Consistency               string                 `json:"consistency"`
	Format                    string                 `json:"format"`
	Checksum                  string                 `json:"checksum,omitempty"`
	PlaintextChecksum         string                 `json:"plaintextChecksum,omitempty"`
	Bytes                     int64                  `json:"bytes"`
	Encryption                string                 `json:"encryption"`
	KeyID                     string                 `json:"keyId"`
	Location                  string                 `json:"location"`
	ImageID                   string                 `json:"imageId,omitempty"`
	Policy                    string                 `json:"policy"`
	CheckCount                int                    `json:"checkCount"`
	VerificationState         string                 `json:"verificationState"`
	CleanupState              string                 `json:"cleanupState"`
	VerificationIntervalHours int                    `json:"verificationIntervalHours"`
	NextVerificationAt        *time.Time             `json:"nextVerificationAt,omitempty"`
	VerifiedAt                *time.Time             `json:"verifiedAt,omitempty"`
	CreatedAt                 time.Time              `json:"createdAt"`
	UpdatedAt                 time.Time              `json:"updatedAt"`
	Message                   string                 `json:"message,omitempty"`
	EncryptedInput            string                 `json:"-"`
}
type BackupIntegrityCheck struct {
	Query    string `json:"query"`
	Expected string `json:"expected"`
}
type WorkloadBackupOperation struct {
	ID                  string    `json:"id"`
	OffsiteStoreID      string    `json:"offsiteStoreId,omitempty"`
	ExecutionServerID   string    `json:"executionServerId,omitempty"`
	ExecutionNodeID     string    `json:"executionNodeId,omitempty"`
	ExecutionGeneration int64     `json:"executionGeneration,omitempty"`
	CapturePolicyID     string    `json:"capturePolicyId,omitempty"`
	BackupID            string    `json:"backupId"`
	ProjectID           string    `json:"projectId"`
	Action              string    `json:"action"`
	State               string    `json:"state"`
	TargetRunID         string    `json:"targetRunId,omitempty"`
	TargetName          string    `json:"targetName,omitempty"`
	TargetResourceID    string    `json:"targetResourceId,omitempty"`
	Revision            int64     `json:"revision"`
	LeaseUntil          time.Time `json:"recoveryAfter"`
	LeaseToken          string    `json:"-"`
	EncryptedInput      string    `json:"-"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
	Message             string    `json:"message,omitempty"`
	CleanupState        string    `json:"cleanupState,omitempty"`
}

// WorkloadBackupRequest is carried only in encrypted recovery records and typed agent jobs.
type WorkloadBackupRequest struct {
	ExecutionNodeGeneration int64                    `json:"executionNodeGeneration,omitempty"`
	OffsiteAccess           *backupstore.Access      `json:"offsiteAccess,omitempty"`
	RecoveryAction          string                   `json:"recoveryAction,omitempty"`
	OperationID             string                   `json:"operationId"`
	Action                  string                   `json:"action"`
	Backup                  WorkloadBackup           `json:"backup"`
	Source                  ServiceProvisionRequest  `json:"source"`
	Storage                 StorageResource          `json:"storage"`
	Key                     string                   `json:"key"`
	Checks                  []BackupIntegrityCheck   `json:"checks,omitempty"`
	Destination             *ServiceProvisionRequest `json:"destination,omitempty"`
	DestinationStorage      *StorageResource         `json:"destinationStorage,omitempty"`
	ExpectedDestination     string                   `json:"expectedDestination,omitempty"`
}
type WorkloadBackupResult struct {
	Offsite           *BackupOffsiteArtifact `json:"offsite,omitempty"`
	BackupID          string                 `json:"backupId"`
	ProjectID         string                 `json:"projectId"`
	OperationID       string                 `json:"operationId"`
	State             string                 `json:"state"`
	ArtifactID        string                 `json:"artifactId"`
	Checksum          string                 `json:"checksum,omitempty"`
	PlaintextChecksum string                 `json:"plaintextChecksum,omitempty"`
	Bytes             int64                  `json:"bytes"`
	ImageID           string                 `json:"imageId,omitempty"`
	CleanupState      string                 `json:"cleanupState"`
	Message           string                 `json:"message"`
}

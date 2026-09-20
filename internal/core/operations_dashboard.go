package core

import "time"

type OperationsAuditSummary struct {
	Since    time.Time    `json:"since"`
	Total    int64        `json:"total"`
	Rejected int64        `json:"rejected"`
	Recent   []AuditEvent `json:"recent"`
}
type OwnershipSummary struct {
	Total      int64 `json:"total"`
	Unassigned int64 `json:"unassigned"`
}
type BackupSummary struct {
	Configured     bool          `json:"configured"`
	Recorded       int64         `json:"recorded"`
	Latest         *BackupRecord `json:"latest,omitempty"`
	LatestVerified *BackupRecord `json:"latestVerified,omitempty"`
}
type OperationsSummary struct {
	ObservedAt time.Time              `json:"observedAt"`
	Audit      OperationsAuditSummary `json:"audit"`
	Ownership  OwnershipSummary       `json:"ownership"`
	Backups    *BackupSummary         `json:"backups,omitempty"`
}
type OwnershipPrincipal struct {
	PrincipalType string    `json:"principalType"`
	PrincipalID   string    `json:"principalId"`
	DisplayName   string    `json:"displayName"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
type OwnershipItem struct {
	AppID     string              `json:"appId"`
	AppName   string              `json:"appName"`
	ProjectID string              `json:"projectId"`
	Owner     *OwnershipPrincipal `json:"owner,omitempty"`
}
type OwnershipFilter struct {
	ProjectIDs           []string
	Query                string
	Unassigned           bool
	Assigned             bool
	BeforeName, BeforeID string
	Limit                int
}

package core

import "time"

type AuditEvent struct {
	ID             string    `json:"id"`
	ActorID        string    `json:"actorId"`
	ActorName      string    `json:"actorName"`
	ImpersonatorID string    `json:"impersonatorId,omitempty"`
	ProjectID      string    `json:"projectId,omitempty"`
	AppID          string    `json:"appId,omitempty"`
	Action         string    `json:"action"`
	ResourceID     string    `json:"resourceId,omitempty"`
	Outcome        string    `json:"outcome"`
	CreatedAt      time.Time `json:"createdAt"`
}
type AuditFilter struct {
	ProjectIDs                     []string
	AppID, ActorID, Action, Before string
	Limit                          int
}
type ApplicationOwner struct {
	AppID         string    `json:"appId"`
	PrincipalType string    `json:"principalType"`
	PrincipalID   string    `json:"principalId"`
	UpdatedAt     time.Time `json:"updatedAt"`
}
type IdentityTeamMapping struct {
	ID            string `json:"id"`
	ProviderID    string `json:"providerId"`
	ExternalGroup string `json:"externalGroup"`
	TeamID        string `json:"teamId"`
}
type RetentionPolicy struct {
	ProjectID string `json:"projectId"`
	LogDays   int    `json:"logDays"`
	RunDays   int    `json:"runDays"`
	KeepRuns  int    `json:"keepRuns"`
}
type RetentionResult struct {
	Logs          int64 `json:"logs"`
	Runs          int64 `json:"runs"`
	ProtectedRuns int64 `json:"protectedRuns"`
	Applied       bool  `json:"applied"`
}
type BackupRecord struct {
	ID         string     `json:"id"`
	Engine     string     `json:"engine"`
	State      string     `json:"state"`
	Bytes      int64      `json:"bytes"`
	CreatedAt  time.Time  `json:"createdAt"`
	VerifiedAt *time.Time `json:"verifiedAt,omitempty"`
	Message    string     `json:"message"`
}

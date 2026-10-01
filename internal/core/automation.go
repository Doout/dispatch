package core

import "time"

const PrincipalServiceAccount = "service_account"
const (
	PermissionInfrastructureInspect Permission = "infrastructure.inspect"
	PermissionInfrastructureCreate  Permission = "infrastructure.create"
	PermissionInfrastructureModify  Permission = "infrastructure.modify"
	PermissionInfrastructureDelete  Permission = "infrastructure.delete"
	PermissionSnapshotCreate        Permission = "infrastructure.snapshot"
	PermissionSnapshotRestore       Permission = "infrastructure.restore"
)

type ServiceAccount struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
type AutomationCredential struct {
	ID         string     `json:"id"`
	AccountID  string     `json:"accountId"`
	Name       string     `json:"name"`
	TokenHash  string     `json:"-"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	CreatedAt  time.Time  `json:"createdAt"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}
type PrincipalGrant struct {
	PrincipalType string       `json:"principalType"`
	PrincipalID   string       `json:"principalId"`
	ProjectID     string       `json:"projectId"`
	Permissions   []Permission `json:"permissions"`
	ExpiresAt     *time.Time   `json:"expiresAt,omitempty"`
	UpdatedAt     time.Time    `json:"updatedAt"`
}
type InfrastructureAssignment struct {
	ProjectID  string    `json:"projectId"`
	Kind       string    `json:"kind"`
	ResourceID string    `json:"resourceId"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func AssignableProjectPermissions() []Permission {
	return []Permission{PermissionProjectView, PermissionProjectConfigure, PermissionDeploymentRun, PermissionDeploymentCancel, PermissionInfrastructureInspect, PermissionInfrastructureCreate, PermissionInfrastructureModify, PermissionInfrastructureDelete, PermissionSnapshotCreate, PermissionSnapshotRestore}
}

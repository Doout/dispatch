package core

import "time"

const (
	UserRoleOwner  = "owner"
	UserRoleMember = "member"

	UserStateActive   = "active"
	UserStatePending  = "pending"
	UserStateDisabled = "disabled"

	TeamMemberRoleMember  = "member"
	TeamMemberRoleManager = "manager"

	PrincipalUser = "user"
	PrincipalTeam = "team"

	ScopeProject = "project"

	RoleProjectAdmin = "admin"
	RoleOperator     = "operator"
	RoleDeployer     = "deployer"
	RoleViewer       = "viewer"
)

type Permission string

const (
	PermissionAccessManage         Permission = "access.manage"
	PermissionProjectView          Permission = "project.view"
	PermissionProjectManage        Permission = "project.manage"
	PermissionProjectAccess        Permission = "project.access"
	PermissionProjectConfigure     Permission = "project.configure"
	PermissionDeploymentRun        Permission = "deployment.run"
	PermissionDeploymentCancel     Permission = "deployment.cancel"
	PermissionStageApprove         Permission = "stage.approve"
	PermissionInfrastructureManage Permission = "infrastructure.manage"
	PermissionSecretsManage        Permission = "secrets.manage"
	PermissionConnectionsManage    Permission = "connections.manage"
)

type User struct {
	ID                 string    `json:"id"`
	Username           string    `json:"username"`
	DisplayName        string    `json:"displayName"`
	Email              string    `json:"email,omitempty"`
	PasswordHash       string    `json:"-"`
	PasswordConfigured bool      `json:"passwordConfigured"`
	SystemRole         string    `json:"systemRole"`
	State              string    `json:"state"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

type Team struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type TeamMember struct {
	TeamID    string    `json:"teamId"`
	UserID    string    `json:"userId"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

type RoleAssignment struct {
	ID            string    `json:"id"`
	PrincipalType string    `json:"principalType"`
	PrincipalID   string    `json:"principalId"`
	ScopeType     string    `json:"scopeType"`
	ScopeID       string    `json:"scopeId"`
	Role          string    `json:"role"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Identity struct {
	ID          string       `json:"id"`
	Username    string       `json:"username"`
	DisplayName string       `json:"displayName"`
	SystemRole  string       `json:"systemRole"`
	Permissions []Permission `json:"permissions"`
}

type RoleDefinition struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

type AccessOverview struct {
	Users       []User             `json:"users"`
	Teams       []Team             `json:"teams"`
	Members     []TeamMember       `json:"members"`
	Assignments []RoleAssignment   `json:"assignments"`
	Roles       []RoleDefinition   `json:"roles"`
	Projects    []Project          `json:"projects"`
	Providers   []AuthProvider     `json:"providers"`
	Identities  []ExternalIdentity `json:"identities"`
}

func ProjectRoles() []RoleDefinition {
	return []RoleDefinition{
		{ID: RoleProjectAdmin, Name: "Project admin", Permissions: []Permission{PermissionProjectView, PermissionProjectManage, PermissionProjectConfigure, PermissionDeploymentRun, PermissionDeploymentCancel, PermissionStageApprove}},
		{ID: RoleOperator, Name: "Operator", Permissions: []Permission{PermissionProjectView, PermissionProjectConfigure, PermissionDeploymentRun, PermissionDeploymentCancel, PermissionStageApprove}},
		{ID: RoleDeployer, Name: "Deployer", Permissions: []Permission{PermissionProjectView, PermissionDeploymentRun, PermissionDeploymentCancel}},
		{ID: RoleViewer, Name: "Viewer", Permissions: []Permission{PermissionProjectView}},
	}
}

func RoleAllows(role string, permission Permission) bool {
	for _, definition := range ProjectRoles() {
		if definition.ID != role {
			continue
		}
		for _, allowed := range definition.Permissions {
			if allowed == permission {
				return true
			}
		}
	}
	return false
}

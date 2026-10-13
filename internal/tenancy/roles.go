package tenancy

import "github.com/doout/dispatch/internal/tenancy/roles"

const (
	RoleOwner         = roles.Owner
	RoleAdmin         = roles.Admin
	RoleMember        = roles.Member
	RolePlatformAdmin = roles.PlatformAdmin
)

func IsTenantRole(slug string) bool {
	return roles.IsTenantRole(slug)
}

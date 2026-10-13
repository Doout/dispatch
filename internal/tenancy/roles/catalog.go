package roles

const (
	Owner         = "owner"
	Admin         = "admin"
	Member        = "member"
	PlatformAdmin = "platform-admin"
)

type Permission struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Definition struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Scope       string   `json:"scope"`
	Permissions []string `json:"permissions"`
}

// Permissions describes control plane access. Project grants stay in the
// tenant runtime and are not published to the identity provider.
func Permissions() []Permission {
	return []Permission{
		{"platform:tenants:read", "View tenant directory", "View tenant metadata without accessing tenant workloads."},
		{"platform:tenants:create", "Create tenants", "Create a tenant and assign its initial owner."},
		{"platform:usage:read", "View tenant usage", "View aggregate tenant usage without accessing tenant data."},
		{"tenant:access", "Open tenant", "Open an active tenant membership; project access requires separate grants."},
		{"tenant:members:read", "View tenant members", "View the members of this tenant."},
		{"tenant:members:manage", "Manage tenant members", "Add, remove and change tenant memberships while retaining an active owner."},
		{"tenant:access-requests:manage", "Manage tenant access requests", "Review and approve requests to join this tenant."},
		{"tenant:runtime:manage", "Manage tenant runtime", "Administer the tenant runtime, including infrastructure, integrations and project access."},
	}
}

// Definitions is the shared catalog for hosted membership validation and WorkOS
// configuration. It describes current access; WorkOS claims do not grant local
// privileges by themselves.
func Definitions() []Definition {
	return []Definition{
		{Owner, "Owner", "Manage this tenant, its runtime, memberships and access requests.", "tenant", []string{"tenant:access", "tenant:members:read", "tenant:members:manage", "tenant:access-requests:manage", "tenant:runtime:manage"}},
		{Admin, "Admin", "Administer this tenant's runtime and view its members.", "tenant", []string{"tenant:access", "tenant:members:read", "tenant:runtime:manage"}},
		{Member, "Member", "Open this tenant and use explicitly assigned projects.", "tenant", []string{"tenant:access"}},
		{PlatformAdmin, "Platform admin", "Create tenants and view their metadata and usage; tenant membership is separate.", "platform", []string{"platform:tenants:read", "platform:tenants:create", "platform:usage:read"}},
	}
}

func IsTenantRole(slug string) bool {
	for _, role := range Definitions() {
		if role.Scope == "tenant" && role.Slug == slug {
			return true
		}
	}
	return false
}

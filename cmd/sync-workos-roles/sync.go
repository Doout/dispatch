package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	catalog "github.com/doout/dispatch/internal/tenancy/roles"
	workos "github.com/workos/workos-go/v10"
)

type change struct {
	kind       string
	role       catalog.Definition
	permission catalog.Permission
	before     []string
}

func (c change) summary() string {
	if c.kind == "set permissions" {
		return fmt.Sprintf("set role %s permissions: [%s] -> [%s]", c.role.Slug, strings.Join(c.before, ", "), strings.Join(c.role.Permissions, ", "))
	}
	if c.permission.Slug != "" {
		return c.kind + " " + c.permission.Slug
	}
	return c.kind + " " + c.role.Slug
}

func (c change) apply(ctx context.Context, client *workos.Client) error {
	a := client.Authorization()
	var err error
	switch c.kind {
	case "create permission":
		_, err = a.CreatePermission(ctx, &workos.AuthorizationCreatePermissionParams{Slug: c.permission.Slug, Name: c.permission.Name, Description: &c.permission.Description})
	case "update permission":
		_, err = a.UpdatePermission(ctx, c.permission.Slug, &workos.AuthorizationUpdatePermissionParams{Name: &c.permission.Name, Description: &c.permission.Description})
	case "create role":
		_, err = a.CreateEnvironmentRole(ctx, &workos.AuthorizationCreateEnvironmentRoleParams{Slug: c.role.Slug, Name: c.role.Name, Description: &c.role.Description})
	case "update role":
		_, err = a.UpdateEnvironmentRole(ctx, c.role.Slug, &workos.AuthorizationUpdateEnvironmentRoleParams{Name: &c.role.Name, Description: &c.role.Description})
	case "set permissions":
		_, err = a.SetEnvironmentRolePermissions(ctx, c.role.Slug, &workos.AuthorizationSetEnvironmentRolePermissionsParams{Permissions: c.role.Permissions})
	default:
		return errors.New("unknown role sync operation")
	}
	return err
}

func notFound(err error) bool {
	var api *workos.APIError
	return errors.As(err, &api) && api.StatusCode == 404
}

func description(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func samePermissions(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func rolePermissions(wanted, current []string) []string {
	result := slices.Clone(wanted)
	for _, p := range current {
		if !strings.HasPrefix(p, "tenant:") && !strings.HasPrefix(p, "platform:") && !slices.Contains(result, p) {
			result = append(result, p)
		}
	}
	return result
}

// Read the entire managed catalog before making any changes. A failed lookup is
// not evidence that a role or permission is missing unless WorkOS returns 404.
func buildPlan(ctx context.Context, client *workos.Client, roles []catalog.Definition, permissions []catalog.Permission) ([]change, error) {
	plan := []change{}
	a := client.Authorization()
	for _, wanted := range permissions {
		current, err := a.GetPermission(ctx, wanted.Slug)
		if notFound(err) {
			plan = append(plan, change{kind: "create permission", permission: wanted})
			continue
		}
		if err != nil {
			return nil, apiError("read permission "+wanted.Slug, err)
		}
		if current.System || (current.ResourceTypeSlug != "" && current.ResourceTypeSlug != "organization") {
			return nil, fmt.Errorf("permission %s belongs to a different resource type or is managed by WorkOS", wanted.Slug)
		}
		if current.Name != wanted.Name || description(current.Description) != wanted.Description {
			plan = append(plan, change{kind: "update permission", permission: wanted})
		}
	}
	for _, wanted := range roles {
		current, err := a.GetEnvironmentRole(ctx, wanted.Slug)
		if notFound(err) {
			plan = append(plan, change{kind: "create role", role: wanted}, change{kind: "set permissions", role: wanted})
			continue
		}
		if err != nil {
			return nil, apiError("read role "+wanted.Slug, err)
		}
		if current.ResourceTypeSlug != "" && current.ResourceTypeSlug != "organization" {
			return nil, fmt.Errorf("role %s belongs to a different resource type", wanted.Slug)
		}
		if current.Name != wanted.Name || description(current.Description) != wanted.Description {
			plan = append(plan, change{kind: "update role", role: wanted})
		}
		wanted.Permissions = rolePermissions(wanted.Permissions, current.Permissions)
		if !samePermissions(current.Permissions, wanted.Permissions) {
			plan = append(plan, change{kind: "set permissions", role: wanted, before: current.Permissions})
		}
	}
	return plan, nil
}

func validateCatalog(roles []catalog.Definition, permissions []catalog.Permission) error {
	roleSlug := regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	permissionSlug := regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]*$`)
	known := map[string]bool{}
	for _, p := range permissions {
		if !permissionSlug.MatchString(p.Slug) || known[p.Slug] || p.Name == "" || p.Description == "" {
			return fmt.Errorf("invalid or duplicate permission definition: %s", p.Slug)
		}
		known[p.Slug] = true
	}
	seen := map[string]bool{}
	for _, r := range roles {
		if !roleSlug.MatchString(r.Slug) || seen[r.Slug] || r.Name == "" || r.Description == "" || (r.Scope != "tenant" && r.Scope != "platform") {
			return fmt.Errorf("invalid or duplicate role definition: %s", r.Slug)
		}
		seen[r.Slug] = true
		assigned := map[string]bool{}
		for _, p := range r.Permissions {
			if !known[p] || assigned[p] || !strings.HasPrefix(p, r.Scope+":") {
				return fmt.Errorf("role %s has an unknown, duplicate or out-of-scope permission: %s", r.Slug, p)
			}
			assigned[p] = true
		}
	}
	return nil
}

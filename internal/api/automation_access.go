package api

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"time"
)

func (a *API) directProjectGrants(ctx context.Context, identity core.Identity) ([]core.PrincipalGrant, error) {
	data, ok := a.store.(store.AutomationStore)
	if !ok {
		return nil, nil
	}
	kind := identity.Kind
	if kind == "" {
		kind = core.PrincipalUser
	}
	grants, err := data.ListPrincipalGrants(ctx, kind, identity.ID)
	if err != nil {
		return nil, err
	}
	valid := []core.PrincipalGrant{}
	now := time.Now()
	for _, g := range grants {
		if g.ExpiresAt == nil || g.ExpiresAt.After(now) {
			valid = append(valid, g)
		}
	}
	return valid, nil
}
func (a *API) directProjectPermission(ctx context.Context, identity core.Identity, permission core.Permission, project string) (bool, error) {
	// Human approval never follows from a token's project grant.
	if identity.Kind == core.PrincipalServiceAccount && permission == core.PermissionStageApprove {
		return false, nil
	}
	grants, err := a.directProjectGrants(ctx, identity)
	if err != nil {
		return false, err
	}
	for _, g := range grants {
		if g.ProjectID == project {
			for _, p := range g.Permissions {
				if p == permission {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
func (a *API) assignedInfrastructure(ctx context.Context, project, kind, id string) (bool, error) {
	if currentIdentity(ctx).SystemRole == core.UserRoleOwner {
		return true, nil
	}
	if kind == "target" {
		target, err := a.store.GetServer(ctx, id)
		if err != nil {
			return false, err
		}
		if target.ProjectID == project {
			return true, nil
		}
	}
	data, ok := a.store.(store.AutomationStore)
	if !ok {
		return false, nil
	}
	assignments, err := data.ListInfrastructureAssignments(ctx, project)
	if err != nil {
		return false, err
	}
	for _, v := range assignments {
		if v.Kind == kind && v.ResourceID == id {
			return true, nil
		}
	}
	return false, nil
}

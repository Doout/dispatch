package hosted

import (
	"context"
	"net/http"
	"strings"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/tenancy"
)

func (s *Server) tenantAuthenticator(tenant tenancy.Tenant) *api.HostedAuth {
	auth := &api.HostedAuth{TenantID: tenant.ID, LoginURL: s.Config.Origin()}
	scope := core.HostedRoutingScope{TenantID: tenant.ID, TenantSlug: tenant.Slug, RootDomain: s.Config.RootDomain}
	auth.RoutingScope = &scope
	auth.ReserveHostname = func(ctx context.Context, route core.ApplicationRoute) error {
		runtime, err := s.runtime(tenant)
		if err != nil {
			return err
		}
		app, err := runtime.Store.GetApp(ctx, route.AppID)
		if err != nil {
			return err
		}
		expected, err := routing.HostedHostname(scope, app)
		if err != nil {
			return err
		}
		if route.Hostname != expected || route.ProjectID != app.ProjectID || route.ServerID != app.ServerID {
			return tenancy.ErrDenied
		}
		return s.Catalog.ReserveWorkloadHost(ctx, tenancy.WorkloadHost{Hostname: route.Hostname, TenantID: tenant.ID, AppID: app.ID, ProjectID: app.ProjectID, ServerID: app.ServerID})
	}
	auth.ReleaseHostname = func(ctx context.Context, app, server string) error {
		return s.Catalog.ReleaseWorkloadHost(ctx, tenant.ID, app, server)
	}

	auth.Authenticate = func(r *http.Request) (core.Identity, error) {
		user, err := s.authenticateSession(r.Context(), requestToken(r, tenantCookie), tenancy.TenantAudience(tenant.ID))
		if err != nil {
			return core.Identity{}, err
		}
		origin, originErr := s.Catalog.LoginOrigin(r.Context(), requestToken(r, tenantCookie))
		if originErr == nil && strings.HasPrefix(origin.Provider, "tenant:") {
			providerID, ok := strings.CutPrefix(origin.Provider, "tenant:"+tenant.ID+":")
			if !ok {
				return core.Identity{}, tenancy.ErrDenied
			}
			runtime, err := s.runtime(tenant)
			if err != nil {
				return core.Identity{}, err
			}
			provider, err := runtime.Store.GetAuthProvider(r.Context(), providerID)
			if err != nil || provider.State != core.AuthProviderStateReady {
				return core.Identity{}, tenancy.ErrDenied
			}
			linked, err := runtime.Store.GetExternalIdentity(r.Context(), providerID, origin.Subject)
			if err != nil || linked.UserID != user.ID {
				return core.Identity{}, tenancy.ErrDenied
			}
		}
		membership, err := s.Catalog.Membership(r.Context(), tenant.ID, user.ID)
		if err != nil || membership.State != tenancy.StateActive {
			return core.Identity{}, tenancy.ErrDenied
		}
		role := core.UserRoleMember
		if membership.Role == tenancy.RoleOwner || membership.Role == tenancy.RoleAdmin {
			role = core.UserRoleOwner
		}
		return core.Identity{ID: user.ID, Kind: core.PrincipalUser, Username: user.Email, DisplayName: user.Name, SystemRole: role}, nil
	}
	auth.EnrollExternal = func(ctx context.Context, providerID, subject, email, name string) (string, error) {
		user, err := s.Catalog.ResolveExternalUser(ctx, tenancy.ExternalLogin{Provider: "tenant:" + tenant.ID + ":" + providerID, Subject: subject, Email: email, Name: name})
		if err != nil {
			return "", err
		}
		runtime, err := s.runtime(tenant)
		if err != nil {
			return "", err
		}
		if err = s.syncUser(ctx, runtime.Store, tenant.ID, user.ID); err != nil {
			return "", err
		}
		return user.ID, nil
	}
	auth.CompleteExternal = func(w http.ResponseWriter, r *http.Request, userID, providerID, subject string) error {
		// A tenant provider can issue only a session for this tenant, never a
		// platform session, even when the linked user is a platform administrator.
		token, err := s.Catalog.CreateExternalSession(r.Context(), userID, tenancy.TenantAudience(tenant.ID), s.Config.SessionDuration, tenancy.LoginOrigin{Provider: "tenant:" + tenant.ID + ":" + providerID, Subject: subject})
		if err != nil {
			return err
		}
		setSession(w, tenantCookie, token, int(s.Config.SessionDuration.Seconds()))
		return nil
	}
	return auth
}

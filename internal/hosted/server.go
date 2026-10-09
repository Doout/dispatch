package hosted

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/tenancy"
	"github.com/doout/dispatch/internal/ui"
	"github.com/go-chi/chi/v5"
)

const platformCookie = "__Host-dispatch-platform"
const tenantCookie = "__Host-dispatch-tenant"

type TenantRuntime struct {
	Handler http.Handler
	Store   *store.SQLStore
	Close   func()
}

type RuntimeFactory func(context.Context, tenancy.Tenant, *api.HostedAuth) (*TenantRuntime, error)

type Server struct {
	Config       Config
	Catalog      *tenancy.Catalog
	OpenRuntime  RuntimeFactory
	Logger       *slog.Logger
	ctx          context.Context
	platform     http.Handler
	mu           sync.Mutex
	runtimes     map[string]*TenantRuntime
	throttle     loginThrottle
	dnsMu        sync.Mutex
	certificates certificateCache
}

func New(ctx context.Context, cfg Config, catalog *tenancy.Catalog, factory RuntimeFactory, logger *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{Config: cfg, Catalog: catalog, OpenRuntime: factory, Logger: logger, ctx: ctx, runtimes: map[string]*TenantRuntime{}}
	s.platform = s.platformRoutes()
	return s, nil
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, runtime := range s.runtimes {
		if runtime.Close != nil {
			runtime.Close()
		}
		delete(s.runtimes, id)
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r = s.clientAddress(r)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
	host, valid := canonicalHost(r.Host)
	if !valid {
		http.NotFound(w, r)
		return
	}
	if host == s.Config.RootDomain {
		if !originAllowed(r, s.Config.Origin()) {
			problem(w, http.StatusForbidden, "Origin not allowed.")
			return
		}
		s.platform.ServeHTTP(w, r)
		return
	}
	slug, found := strings.CutSuffix(host, "."+s.Config.RootDomain)
	if !found || strings.Contains(slug, ".") {
		http.NotFound(w, r)
		return
	}
	tenant, err := s.Catalog.TenantBySlug(r.Context(), slug)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !originAllowed(r, s.Config.TenantOrigin(slug)) {
		problem(w, http.StatusForbidden, "Origin not allowed.")
		return
	}
	if r.URL.Path == "/api/v1/hosted" && r.Method == http.MethodGet {
		respond(w, http.StatusOK, map[string]any{"mode": "tenant", "tenant": tenant, "loginUrl": s.Config.Origin(), "origin": s.Config.TenantOrigin(slug)})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/hosted/auth/") {
		s.tenantAuth(w, r, tenant)
		return
	}
	if tenant.State != tenancy.StateActive {
		problem(w, http.StatusServiceUnavailable, "This tenant is being prepared.")
		return
	}
	if r.URL.Path == "/api/v1/hosted/dns" {
		s.tenantDNS(w, r, tenant)
		return
	}
	if r.URL.Path == "/api/v1/hosted/certificate" {
		s.tenantCertificate(w, r, tenant)
		return
	}
	runtime, err := s.runtime(tenant)
	if err != nil {
		s.Logger.Error("tenant controller unavailable", "tenant", tenant.ID)
		problem(w, http.StatusServiceUnavailable, "Tenant unavailable.")
		return
	}
	runtime.Handler.ServeHTTP(w, r)
}

func (s *Server) runtime(tenant tenancy.Tenant) (*TenantRuntime, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if runtime := s.runtimes[tenant.ID]; runtime != nil {
		return runtime, nil
	}
	auth := &api.HostedAuth{TenantID: tenant.ID, LoginURL: s.Config.Origin()}
	auth.Authenticate = func(r *http.Request) (core.Identity, error) {
		user, err := s.Catalog.AuthenticateSession(r.Context(), requestToken(r, tenantCookie), tenancy.TenantAudience(tenant.ID))
		if err != nil {
			return core.Identity{}, err
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
	runtime, err := s.OpenRuntime(s.ctx, tenant, auth)
	if err != nil {
		return nil, err
	}
	members, err := s.Catalog.TenantMembers(s.ctx, tenant.ID)
	if err == nil {
		for _, member := range members {
			if err = s.syncUser(s.ctx, runtime.Store, tenant.ID, member.UserID); err != nil {
				break
			}
		}
	}
	if err != nil {
		if runtime.Close != nil {
			runtime.Close()
		}
		return nil, err
	}
	s.runtimes[tenant.ID] = runtime
	return runtime, nil
}

func (s *Server) platformRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/v1/hosted", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{"mode": "platform", "origin": s.Config.Origin(), "registrationEnabled": s.Config.SMTP.Address != ""})
	})
	r.Post("/api/v1/account/login", s.login)
	r.Post("/api/v1/account/register", s.register)
	r.Post("/api/v1/account/verify-email", s.verifyEmail)
	r.Post("/api/v1/account/logout", s.logout)
	r.Get("/api/v1/account", s.account)
	r.Put("/api/v1/account/password", s.changePassword)
	r.Get("/api/v1/account/tenants", s.memberships)
	r.Post("/api/v1/account/handoffs", s.handoff)
	r.Post("/api/v1/account/invitations/{id}/accept", s.acceptInvitation)
	r.Get("/api/v1/platform/tenants", s.listTenants)
	r.Post("/api/v1/platform/tenants", s.createTenant)
	r.Get("/api/v1/platform/tenants/{id}", s.tenantMetadata)
	r.Get("/api/v1/platform/tenants/{id}/usage", s.tenantUsage)
	r.Get("/api/v1/tenants/{id}/members", s.listMembers)
	r.Post("/api/v1/tenants/{id}/members", s.addMember)
	r.Put("/api/v1/tenants/{id}/members/{userId}", s.setMember)
	r.Delete("/api/v1/tenants/{id}/members/{userId}", s.deleteMember)
	r.Handle("/api/v1/internal/dns/snapshot", s.dnsSnapshotHandler())
	// Unknown API paths never fall through to the SPA or to a tenant controller.
	r.HandleFunc("/api/*", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	r.Handle("/*", ui.Handler())
	return r
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) (tenancy.User, bool) {
	user, err := s.Catalog.AuthenticateSession(r.Context(), requestToken(r, platformCookie), tenancy.AudiencePlatform)
	if err != nil {
		problem(w, http.StatusUnauthorized, "Sign in to continue.")
		return tenancy.User{}, false
	}
	return user, true
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) (tenancy.User, bool) {
	user, ok := s.user(w, r)
	if !ok {
		return user, false
	}
	if !user.PlatformAdmin {
		problem(w, http.StatusForbidden, "Platform access required.")
		return user, false
	}
	return user, true
}

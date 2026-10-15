package hosted

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/instancebridge"
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

type runtimeInitialization struct {
	ready   chan struct{}
	runtime *TenantRuntime
	err     error
}

var errServerClosed = errors.New("hosted server closed")

type Server struct {
	connectionMu      sync.Mutex
	connections       map[*instanceConnectionWatch]struct{}
	connectionsDone   chan struct{}
	Bridge            *instancebridge.Broker
	workloadTransport http.RoundTripper
	workosSessionMu   sync.Mutex
	workosSessions    map[string]time.Time
	workos            workOSService
	workosMu          sync.Mutex
	Config            Config
	Catalog           *tenancy.Catalog
	OpenRuntime       RuntimeFactory
	Logger            *slog.Logger
	ctx               context.Context
	cancel            context.CancelFunc
	platform          http.Handler
	mu                sync.Mutex
	runtimes          map[string]*TenantRuntime
	initializing      map[string]*runtimeInitialization
	closed            bool
	closeDone         chan struct{}
	throttle          loginThrottle
	dnsMu             sync.Mutex
	certificates      certificateCache
}

func New(ctx context.Context, cfg Config, catalog *tenancy.Catalog, factory RuntimeFactory, logger *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{Bridge: instancebridge.NewBroker(), Config: cfg, Catalog: catalog, OpenRuntime: factory, Logger: logger, ctx: ctx, cancel: cancel, runtimes: map[string]*TenantRuntime{}, initializing: map[string]*runtimeInitialization{}, closeDone: make(chan struct{})}
	if cfg.WorkOS.enabled() {
		s.workos = newWorkOSClient(cfg.WorkOS)
	}
	s.connections = map[*instanceConnectionWatch]struct{}{}
	s.connectionsDone = make(chan struct{})
	go s.watchInstanceConnections()
	s.platform = s.platformRoutes()
	return s, nil
}

func (s *Server) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.closeDone
		return
	}
	s.closed = true
	runtimes := s.runtimes
	s.runtimes = nil
	pending := make([]<-chan struct{}, 0, len(s.initializing))
	for _, initialization := range s.initializing {
		pending = append(pending, initialization.ready)
	}
	s.mu.Unlock()

	s.cancel()
	s.Bridge.Close()
	<-s.connectionsDone
	for _, runtime := range runtimes {
		if runtime.Close != nil {
			runtime.Close()
		}
	}
	for _, ready := range pending {
		<-ready
	}
	close(s.closeDone)
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
	if found && !strings.Contains(slug, ".") && tenancy.ReservedTenantPrefix(slug) {
		s.workload(w, r, host)
		return
	}
	if !found || strings.Contains(slug, ".") || s.Config.TenantNameReserved(slug) {
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
		native := false
		backendMode := s.Config.BackendMode
		if connection, err := s.Catalog.InstanceConnection(r.Context(), tenant.ID); err == nil {
			backendMode = connection.BackendMode
			native = connection.BackendMode == tenancy.BackendConnected && connection.RemoteAccess && s.Bridge.NativeSignInAvailable(connectionKey(connection))
		}
		source := "doodship"
		if cookie, err := r.Cookie(instancebridge.NativeSessionCookie); native && err == nil && cookie.Value != "" {
			source = "instance"
		}
		respond(w, http.StatusOK, map[string]any{"mode": "tenant", "tenant": tenant, "loginUrl": s.Config.Origin(), "origin": s.Config.TenantOrigin(slug), "nativeSignInAvailable": native, "backendMode": backendMode, "authentication": source})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/hosted/auth/") {
		s.tenantAuth(w, r, tenant)
		return
	}
	if tenant.State != tenancy.StateActive {
		problem(w, http.StatusServiceUnavailable, "This instance is being set up.")
		return
	}
	if r.URL.Path == "/api/v1/hosted/dns" || r.URL.Path == "/api/v1/hosted/certificate" {
		http.NotFound(w, r)
		return
	}
	connection, err := s.Catalog.InstanceConnection(r.Context(), tenant.ID)
	if err != nil {
		catalogError(w, err)
		return
	}
	if connection.BackendMode == tenancy.BackendConnected {
		s.connectedInstance(w, r, tenant, connection)
		return
	}
	if s.Config.BackendMode == tenancy.BackendConnected {
		problem(w, 503, "This instance needs to be connected to its self-hosted installation.")
		return
	}
	runtime, err := s.runtime(tenant)
	if err != nil {
		s.Logger.Error("tenant controller unavailable", "tenant", tenant.ID)
		problem(w, http.StatusServiceUnavailable, "Instance unavailable.")
		return
	}
	runtime.Handler.ServeHTTP(w, r)
}

func (s *Server) runtime(tenant tenancy.Tenant) (*TenantRuntime, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errServerClosed
	}
	if runtime := s.runtimes[tenant.ID]; runtime != nil {
		s.mu.Unlock()
		return runtime, nil
	}
	if initialization := s.initializing[tenant.ID]; initialization != nil {
		s.mu.Unlock()
		select {
		case <-initialization.ready:
			return initialization.runtime, initialization.err
		case <-s.ctx.Done():
			return nil, s.ctx.Err()
		}
	}
	initialization := &runtimeInitialization{ready: make(chan struct{})}
	s.initializing[tenant.ID] = initialization
	s.mu.Unlock()

	runtime, err := s.openRuntime(tenant)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if runtime != nil && runtime.Close != nil {
			runtime.Close()
		}
		runtime, err = nil, errServerClosed
		s.mu.Lock()
	} else if err == nil {
		s.runtimes[tenant.ID] = runtime
	}
	initialization.runtime, initialization.err = runtime, err
	delete(s.initializing, tenant.ID)
	close(initialization.ready)
	s.mu.Unlock()
	return runtime, err
}

func (s *Server) openRuntime(tenant tenancy.Tenant) (*TenantRuntime, error) {
	if s.Config.BackendMode == tenancy.BackendConnected || s.OpenRuntime == nil {
		return nil, errors.New("embedded backend disabled")
	}
	auth := s.tenantAuthenticator(tenant)
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
	return runtime, nil
}

func (s *Server) platformRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/readyz", s.readiness)
	r.Get("/api/v1/tenants/{id}/connection", s.getInstanceConnection)
	r.Post("/api/v1/tenants/{id}/connection", s.createInstanceEnrollment)
	r.Delete("/api/v1/tenants/{id}/connection", s.disconnectInstance)
	r.Post("/api/v1/instance-connections/enroll", s.enrollInstance)
	r.Get("/api/v1/instance-connections/ws", s.instanceSocket)
	r.Put("/api/v1/instance-connections/policy", s.updateInstancePolicy)
	r.Delete("/api/v1/instance-connections/current", s.disconnectCurrentInstance)
	r.Get("/api/v1/instance-connections/routes", s.listInstanceRoutes)
	r.Post("/api/v1/instance-connections/routes", s.publishInstanceRoute)
	r.Delete("/api/v1/instance-connections/routes", s.removeInstanceRoute)
	r.Post("/api/v1/instance-connections/usage", s.reportInstanceUsage)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/v1/hosted", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]any{"mode": "platform", "backendMode": s.Config.BackendMode, "origin": s.Config.Origin(), "reservedTenantNames": s.Config.ReservedTenantNames, "registrationEnabled": s.Config.AllowRegistration && s.passwordLoginAllowed() && s.Config.SMTP.Address != "", "authkitEnabled": s.workos != nil, "workosLoginMethods": s.Config.WorkOS.loginMethods(), "passwordLoginEnabled": s.passwordLoginAllowed(), "waitlistEnabled": s.workos != nil && !s.Config.AllowRegistration})
	})
	r.Post("/api/v1/account/workos/authenticate", s.managedLogin)
	r.Post("/api/v1/account/workos/invitation", s.managedInvitation)
	r.Post("/api/v1/account/workos/waitlist", s.managedWaitlist)
	r.Post("/api/v1/account/workos/password-reset", s.managedPasswordReset)
	r.Post("/api/v1/account/workos/password-reset/confirm", s.managedConfirmPasswordReset)
	r.Post("/api/v1/account/workos/start", s.startWorkOS)
	r.Get("/api/v1/account/workos/callback", s.completeWorkOS)
	r.Post("/api/v1/account/login", s.login)
	r.Post("/api/v1/account/register", s.register)
	r.Post("/api/v1/account/verify-email", s.verifyEmail)
	r.Post("/api/v1/account/logout", s.logout)
	r.Get("/api/v1/account", s.account)
	r.Put("/api/v1/account/password", s.changePassword)
	r.Get("/api/v1/account/tenants", s.memberships)
	r.Post("/api/v1/account/tenants", s.createOwnTenant)
	r.Post("/api/v1/account/join-requests", s.requestTenantJoin)
	r.Post("/api/v1/account/handoffs", s.handoff)
	r.Post("/api/v1/account/invitations/{id}/accept", s.acceptInvitation)
	r.Get("/api/v1/platform/tenants", s.listTenants)
	r.Post("/api/v1/platform/tenants", s.createTenant)
	r.Get("/api/v1/platform/tenants/{id}", s.tenantMetadata)
	r.Get("/api/v1/platform/tenants/{id}/usage", s.tenantUsage)
	r.Get("/api/v1/tenants/{id}/join-requests", s.listJoinRequests)
	r.Delete("/api/v1/tenants/{id}", s.deleteInstance)
	r.Get("/api/v1/tenants/{id}/members", s.listMembers)
	r.Post("/api/v1/tenants/{id}/members", s.addMember)
	r.Put("/api/v1/tenants/{id}/members/{userId}", s.setMember)
	r.Delete("/api/v1/tenants/{id}/members/{userId}", s.deleteMember)
	// Unknown API paths never fall through to the SPA or to a tenant controller.
	r.HandleFunc("/api/*", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	r.Handle("/*", ui.Handler())
	return r
}

func (s *Server) user(w http.ResponseWriter, r *http.Request) (tenancy.User, bool) {
	user, err := s.authenticateSession(r.Context(), requestToken(r, platformCookie), tenancy.AudiencePlatform)
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

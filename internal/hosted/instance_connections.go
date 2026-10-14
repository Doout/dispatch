package hosted

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/instancebridge"
	"github.com/doout/dispatch/internal/routing"
	"github.com/doout/dispatch/internal/tenancy"
	"github.com/doout/dispatch/internal/ui"
	"github.com/go-chi/chi/v5"
)

type instanceConnectionStatus struct {
	State        string `json:"state"`
	RemoteAccess bool   `json:"remoteAccess"`
	InstanceURL  string `json:"instanceUrl"`
	BackendMode  string `json:"backendMode"`
}

func connectionKey(v tenancy.InstanceConnection) string {
	return fmt.Sprintf("%s:%d", v.TenantID, v.Generation)
}
func (s *Server) connectionStatus(v tenancy.InstanceConnection) instanceConnectionStatus {
	state := "not_connected"
	if v.CredentialHash != "" {
		state = "offline"
		if s.Bridge.Connected(connectionKey(v)) {
			state = "connected"
		}
	}
	return instanceConnectionStatus{State: state, RemoteAccess: v.RemoteAccess, InstanceURL: v.DirectURL, BackendMode: v.BackendMode}
}
func (s *Server) getInstanceConnection(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.Catalog.Membership(r.Context(), id, u.ID); err != nil {
		catalogError(w, tenancy.ErrDenied)
		return
	}
	v, err := s.Catalog.InstanceConnection(r.Context(), id)
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, 200, s.connectionStatus(v))
}
func (s *Server) createInstanceEnrollment(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	token, expires, err := s.Catalog.CreateInstanceEnrollment(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, 201, map[string]any{"enrollmentToken": token, "expiresAt": expires, "platformURL": s.Config.Origin(), "subject": u.ID})
}
func (s *Server) disconnectInstance(w http.ResponseWriter, r *http.Request) {
	u, ok := s.user(w, r)
	if !ok {
		return
	}
	v, err := s.Catalog.RevokeInstance(r.Context(), u.ID, chi.URLParam(r, "id"))
	if err != nil {
		catalogError(w, err)
		return
	}
	s.disconnectInstanceStreams(connectionKey(v))
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) enrollInstance(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" || r.Header.Get("Cookie") != "" {
		problem(w, 403, "Use the self-hosted installation to connect.")
		return
	}
	if !s.throttle.allow(r, "instance-enrollment") {
		problem(w, 429, "Wait before trying again.")
		return
	}
	var in struct {
		EnrollmentToken string `json:"enrollmentToken"`
		InstallationID  string `json:"installationID"`
		DirectURL       string `json:"directURL"`
		RemoteAccess    bool   `json:"remoteAccess"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, credential, err := s.Catalog.EnrollInstance(r.Context(), in.EnrollmentToken, in.InstallationID, in.DirectURL, in.RemoteAccess)
	if err != nil {
		catalogError(w, err)
		return
	}
	tenant, err := s.Catalog.Tenant(r.Context(), v.TenantID)
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, 201, map[string]any{"instanceSlug": tenant.Slug, "rootDomain": s.Config.RootDomain, "instanceID": v.TenantID, "credential": credential, "generation": v.Generation, "ownerSubject": v.OwnerSubject, "websocketURL": "wss://" + s.Config.RootDomain + "/api/v1/instance-connections/ws"})
}
func (s *Server) connectionCredential(w http.ResponseWriter, r *http.Request) (tenancy.InstanceConnection, bool) {
	if (r.Header.Get("Origin") != "" && (r.URL.Path != "/api/v1/instance-connections/ws" || r.Header.Get("Origin") != s.Config.Origin())) || r.Header.Get("Cookie") != "" {
		problem(w, 403, "Browser connections are not allowed.")
		return tenancy.InstanceConnection{}, false
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		problem(w, 401, "Connection credential required.")
		return tenancy.InstanceConnection{}, false
	}
	v, err := s.Catalog.AuthenticateInstance(r.Context(), token)
	if err != nil {
		problem(w, 401, "Connection credential was not accepted.")
		return v, false
	}
	return v, true
}
func (s *Server) instanceSocket(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	watch := &instanceConnectionWatch{key: connectionKey(v), credential: v.CredentialHash, cancel: cancel}
	s.connectionMu.Lock()
	s.connections[watch] = struct{}{}
	s.connectionMu.Unlock()
	defer func() { s.connectionMu.Lock(); delete(s.connections, watch); s.connectionMu.Unlock() }()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()

	current, err := s.Catalog.InstanceConnection(ctx, v.TenantID)
	if err != nil || current.Generation != v.Generation || current.CredentialHash != v.CredentialHash {
		problem(w, http.StatusUnauthorized, "Connection credential was not accepted.")
		return
	}

	s.Bridge.ServeConnection(w, r.WithContext(ctx), connectionKey(v))
}
func (s *Server) updateInstancePolicy(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	var in struct {
		DirectURL    string `json:"directURL"`
		RemoteAccess bool   `json:"remoteAccess"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Catalog.UpdateInstancePolicy(r.Context(), v, in.DirectURL, in.RemoteAccess); err != nil {
		catalogError(w, err)
		return
	}
	if v.RemoteAccess != in.RemoteAccess || v.DirectURL != in.DirectURL {
		s.disconnectInstanceStreams(connectionKey(v))
	}
	w.WriteHeader(204)
}
func (s *Server) disconnectCurrentInstance(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	if err := s.Catalog.RevokeInstanceCredential(r.Context(), v); err != nil {
		catalogError(w, err)
		return
	}
	s.disconnectInstanceStreams(connectionKey(v))
	w.WriteHeader(204)
}
func instanceWebhook(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.URL.Path == "/api/v1/events/github" || strings.HasPrefix(r.URL.Path, "/api/v1/events/github/apps/") && len(strings.TrimPrefix(r.URL.Path, "/api/v1/events/github/apps/")) > 0 && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/api/v1/events/github/apps/"), "/"))
}
func (s *Server) connectedInstance(w http.ResponseWriter, r *http.Request, t tenancy.Tenant, v tenancy.InstanceConnection) {
	if instancebridge.NetworkRequestAllowed(r) {
		s.Bridge.ProxyOperation(w, r, connectionKey(v), instancebridge.OperationNetwork, instancebridge.Identity{})
		return
	}
	if instanceWebhook(r) {
		s.Bridge.ProxyOperation(w, r, connectionKey(v), instancebridge.OperationWebhook, instancebridge.Identity{})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		if !v.RemoteAccess {
			if v.DirectURL != "" {
				http.Redirect(w, r, v.DirectURL, http.StatusSeeOther)
			} else {
				problem(w, 503, "Connect a self-hosted installation to open this instance.")
			}
			return
		}
		ui.Handler().ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/api/v1/auth/status" && r.Method == http.MethodGet {
		respond(w, 200, map[string]any{"hosted": true, "setupRequired": false, "tokenLoginAvailable": false, "loginUrl": s.Config.Origin()})
		return
	}
	if !v.RemoteAccess {
		problem(w, 403, "Remote access is disabled. Open the instance at its configured address.")
		return
	}
	if r.URL.Path == "/api/v1/auth/providers" && r.Method == http.MethodGet {
		// Remote console access uses Doodship identities and approved local user
		// mappings. Installation-specific login providers belong to its own URL.
		respond(w, http.StatusOK, []any{})
		return
	}
	if r.URL.Path == "/api/v1/instance-connection" || strings.HasPrefix(r.URL.Path, "/api/v1/instance-connection/") {
		problem(w, 403, "Manage the connection from the self-hosted installation.")
		return
	}
	token := requestToken(r, tenantCookie)
	u, err := s.authenticateSession(r.Context(), token, tenancy.TenantAudience(t.ID))
	if err != nil {
		problem(w, 401, "Sign in to open this instance.")
		return
	}
	m, err := s.Catalog.Membership(r.Context(), t.ID, u.ID)
	if err != nil {
		problem(w, 403, "Instance membership required.")
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, e := s.Catalog.InstanceConnection(ctx, t.ID)
				_, sessionErr := s.authenticateSession(ctx, token, tenancy.TenantAudience(t.ID))
				member, memberErr := s.Catalog.Membership(ctx, t.ID, u.ID)
				if e != nil || current.Generation != v.Generation || current.CredentialHash == "" || !current.RemoteAccess || sessionErr != nil || memberErr != nil || member.Version != m.Version {
					cancel()
					return
				}
			}
		}
	}()
	s.Bridge.Proxy(w, r.WithContext(ctx), connectionKey(v), instancebridge.Identity{UserID: u.ID, Name: u.Name, Email: u.Email, Role: m.Role})
}

func (s *Server) publishInstanceRoute(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	var in struct {
		AppID         string `json:"appID"`
		ProjectID     string `json:"projectID"`
		ServerID      string `json:"serverID"`
		PreviewNumber int    `json:"previewNumber"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.PreviewNumber < 0 || in.PreviewNumber > 2147483647 || len(in.AppID) > 120 || len(in.ProjectID) > 120 || len(in.ServerID) > 120 {
		catalogError(w, tenancy.ErrInvalid)
		return
	}
	tenant, err := s.Catalog.Tenant(r.Context(), v.TenantID)
	if err != nil {
		catalogError(w, err)
		return
	}
	app := core.App{ID: in.AppID, ProjectID: in.ProjectID, ServerID: in.ServerID}
	if in.PreviewNumber > 0 {
		app.Generated = true
		app.HelmProvenance.PullRequests = []core.HelmPullRequest{{Number: in.PreviewNumber}}
	}
	hostname, err := routing.HostedHostname(core.HostedRoutingScope{TenantID: v.TenantID, TenantSlug: tenant.Slug, RootDomain: s.Config.RootDomain}, app)
	if err != nil {
		catalogError(w, tenancy.ErrInvalid)
		return
	}
	if err = s.Catalog.ReserveWorkloadHost(r.Context(), tenancy.WorkloadHost{Hostname: hostname, TenantID: v.TenantID, AppID: in.AppID, ProjectID: in.ProjectID, ServerID: in.ServerID}); err != nil {
		catalogError(w, err)
		return
	}
	respond(w, 201, map[string]string{"hostname": hostname})
}
func (s *Server) removeInstanceRoute(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	var in struct {
		AppID    string `json:"appID"`
		ServerID string `json:"serverID"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.AppID == "" || in.ServerID == "" {
		catalogError(w, tenancy.ErrInvalid)
		return
	}
	if err := s.Catalog.ReleaseWorkloadHost(r.Context(), v.TenantID, in.AppID, in.ServerID); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) reportInstanceUsage(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	var usage tenancy.Usage
	if !decode(w, r, &usage) {
		return
	}
	now := time.Now().UTC()
	if usage.TenantID != "" && usage.TenantID != v.TenantID || usage.PeriodStart.Before(now.Add(-7*24*time.Hour)) || usage.PeriodEnd.After(now.Add(24*time.Hour)) || usage.PeriodEnd.Sub(usage.PeriodStart) != 24*time.Hour || !usage.PeriodStart.Equal(usage.PeriodStart.UTC().Truncate(24*time.Hour)) {
		catalogError(w, tenancy.ErrInvalid)
		return
	}
	usage.TenantID = v.TenantID
	if err := s.Catalog.SaveUsage(r.Context(), usage); err != nil {
		catalogError(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) listInstanceRoutes(w http.ResponseWriter, r *http.Request) {
	v, ok := s.connectionCredential(w, r)
	if !ok {
		return
	}
	hosts, err := s.Catalog.InstanceWorkloadHosts(r.Context(), v.TenantID)
	if err != nil {
		catalogError(w, err)
		return
	}
	type route struct {
		Hostname  string `json:"hostname"`
		AppID     string `json:"appID"`
		ProjectID string `json:"projectID"`
		ServerID  string `json:"serverID"`
	}
	out := make([]route, 0, len(hosts))
	for _, host := range hosts {
		out = append(out, route{host.Hostname, host.AppID, host.ProjectID, host.ServerID})
	}
	respond(w, 200, out)
}

type instanceConnectionWatch struct {
	key        string
	credential string
	cancel     context.CancelFunc
}

func (s *Server) watchInstanceConnections() {
	defer close(s.connectionsDone)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.checkInstanceConnections()
		}
	}
}
func (s *Server) checkInstanceConnections() {
	s.connectionMu.Lock()
	watches := make([]*instanceConnectionWatch, 0, len(s.connections))
	for w := range s.connections {
		watches = append(watches, w)
	}
	s.connectionMu.Unlock()
	if len(watches) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
	defer cancel()
	grants, err := s.Catalog.ActiveInstanceGrants(ctx)
	for _, watch := range watches {
		if err != nil || grants[watch.key] != watch.credential {
			watch.cancel()
			s.Bridge.Disconnect(watch.key)
		}
	}
}

// Cancel watches before removing the broker session so an authenticated request
// paused during its upgrade cannot install a connection after revocation.
func (s *Server) disconnectInstanceStreams(key string) {
	s.connectionMu.Lock()
	for watch := range s.connections {
		if watch.key == key {
			watch.cancel()
		}
	}
	s.connectionMu.Unlock()
	s.Bridge.Disconnect(key)
}

package hosted

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	"golang.org/x/net/websocket"
)

func TestConnectedInstanceNeverOpensCentralRuntime(t *testing.T) {
	f := newHostedFixture(t)
	f.server.Config.BackendMode = tenancy.BackendConnected
	tenant := f.tenant(t, "connected", "owner-a")
	if err := f.server.ReconcileTenants(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.server.CollectUsage(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if f.opens.Load() != 0 {
		t.Fatal("central operational runtime opened")
	}
	v, err := f.catalog.InstanceConnection(context.Background(), tenant.ID)
	if err != nil || v.BackendMode != tenancy.BackendConnected {
		t.Fatalf("mode: %+v %v", v, err)
	}
	owner := f.session(t, "owner-a", tenancy.AudiencePlatform)
	admin := f.session(t, "platform", tenancy.AudiencePlatform)
	path := fmt.Sprintf("/api/v1/tenants/%s/connection", tenant.ID)
	if w := f.request(t, f.server.Config.RootDomain, "POST", path, admin, nil); w.Code != 403 {
		t.Fatalf("platform admin granted enrollment: %d %s", w.Code, w.Body.String())
	}
	w := f.request(t, f.server.Config.RootDomain, "POST", path, owner, nil)
	if w.Code != 201 {
		t.Fatalf("owner enrollment: %d %s", w.Code, w.Body.String())
	}
	var enrollment struct {
		Token string `json:"enrollmentToken"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &enrollment); err != nil {
		t.Fatal(err)
	}
	var requests int
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer local.Close()
	w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/instance-connections/enroll", "", map[string]any{"enrollmentToken": enrollment.Token, "installationID": "selfhosted", "directURL": local.URL, "remoteAccess": false})
	if w.Code != 201 {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	var enrolled struct {
		Credential string `json:"credential"`
	}
	json.Unmarshal(w.Body.Bytes(), &enrolled)
	host := tenant.Slug + "." + f.server.Config.RootDomain
	w = f.request(t, host, "GET", "/", "", nil)
	if w.Code != 303 || w.Header().Get("Location") != local.URL {
		t.Fatalf("direct access: %d %+v", w.Code, w.Header())
	}
	if requests != 0 {
		t.Fatal("control plane fetched customer URL")
	}
	w = f.request(t, host, "POST", "/api/v1/events/github", "", map[string]string{"hello": "webhook"})
	if w.Code != 503 {
		t.Fatalf("offline webhook falsely accepted: %d", w.Code)
	}
	w = f.request(t, host, "POST", "/api/v1/edge/nodes/worker/session", "", map[string]string{"proof": "signed"})
	if w.Code != 503 {
		t.Fatalf("offline worker request not forwarded: %d", w.Code)
	}
	w = f.request(t, host, "GET", "/api/v1/edge/nodes/worker/unsupported", "", nil)
	if w.Code != 403 {
		t.Fatalf("unsupported anonymous network request forwarded: %d", w.Code)
	}
	w = f.request(t, host, "GET", "/api/v1/overview", "", nil)
	if w.Code != 403 {
		t.Fatalf("remote disabled: %d", w.Code)
	}
	w = f.request(t, host, "GET", "/api/v1/auth/providers", "", nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote login offered while disabled: %d", w.Code)
	}
	w = f.request(t, f.server.Config.RootDomain, "PUT", "/api/v1/instance-connections/policy", enrolled.Credential, map[string]any{"directURL": local.URL, "remoteAccess": true})
	if w.Code != 204 {
		t.Fatalf("policy: %d %s", w.Code, w.Body.String())
	}
	w = f.request(t, host, "GET", "/api/v1/auth/providers", "", nil)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("public remote login providers: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/v1/auth/providers/local/start", "/api/v1/auth/providers"} {
		w = f.request(t, host, "POST", path, "", map[string]string{})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous login operation %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = f.request(t, host, "GET", "/api/v1/overview", "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous instance data: %d", w.Code)
	}
	token := f.session(t, "owner-a", tenancy.TenantAudience(tenant.ID))
	w = f.request(t, host, "GET", "/api/v1/overview", token, nil)
	if w.Code != 503 {
		t.Fatalf("offline authenticated request: %d", w.Code)
	}
	w = f.request(t, host, "GET", "/api/v1/overview", admin, nil)
	if w.Code != 401 {
		t.Fatalf("platform session bypassed tenant auth: %d", w.Code)
	}
	w = f.request(t, f.server.Config.RootDomain, "DELETE", path, owner, nil)
	if w.Code != 204 {
		t.Fatalf("disconnect: %d", w.Code)
	}
	w = f.request(t, host, "GET", "/api/v1/overview", token, nil)
	if w.Code != 403 && w.Code != 401 {
		t.Fatalf("revoked session accepted: %d", w.Code)
	}
	if f.opens.Load() != 0 {
		t.Fatal("central backend opened after requests")
	}
}
func TestInstanceCredentialEndpointRejectsBrowser(t *testing.T) {
	f := newHostedFixture(t)
	r := httptest.NewRequest("GET", "https://"+f.server.Config.RootDomain+"/api/v1/instance-connections/ws", nil)
	r.Header.Set("Origin", "https://foreign.example.test")
	r.Header.Set("Authorization", "Bearer guessed")
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if strings.Contains(w.Body.String(), "guessed") {
		t.Fatal("credential reflected")
	}
}

func TestConnectedRouteOwnershipAndLegacyBackend(t *testing.T) {
	f := newHostedFixture(t)
	legacy := f.tenant(t, "legacy", "owner-a")
	f.server.Config.BackendMode = tenancy.BackendConnected
	token := f.session(t, "owner-a", tenancy.TenantAudience(legacy.ID))
	w := f.request(t, legacy.Slug+"."+f.server.Config.RootDomain, "GET", "/api/v1/overview", token, nil)
	if w.Code != 503 || f.opens.Load() != 0 {
		t.Fatalf("legacy data silently reopened %d", w.Code)
	}
	tenant := f.tenant(t, "connected", "owner-a")
	enrollment, _, err := f.catalog.CreateInstanceEnrollment(context.Background(), "owner-a", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := f.catalog.EnrollInstance(context.Background(), enrollment, "installation", "https://customer.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/instance-connections/routes", credential, map[string]any{"appID": "application", "projectID": "project", "serverID": "worker", "previewNumber": 223})
	if w.Code != 201 {
		t.Fatalf("publish %d %s", w.Code, w.Body.String())
	}
	var published struct {
		Hostname string `json:"hostname"`
	}
	json.Unmarshal(w.Body.Bytes(), &published)
	if !strings.HasPrefix(published.Hostname, "p223-") || !strings.HasSuffix(published.Hostname, "-connected."+f.server.Config.RootDomain) {
		t.Fatal(published.Hostname)
	}
	w = f.request(t, published.Hostname, "GET", "/", "", nil)
	if w.Code != 503 {
		t.Fatalf("published workload requires user remoteAccess: %d %s", w.Code, w.Body.String())
	}
	w = f.request(t, f.server.Config.RootDomain, "GET", "/api/v1/instance-connections/routes", credential, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), published.Hostname) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.request(t, f.server.Config.RootDomain, "DELETE", "/api/v1/instance-connections/routes", credential, map[string]string{"appID": "application", "serverID": "worker"})
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.request(t, published.Hostname, "GET", "/", "", nil)
	if w.Code != 404 {
		t.Fatalf("released route reachable: %d", w.Code)
	}
	w = f.request(t, tenant.Slug+"."+f.server.Config.RootDomain, "GET", "/api/v1/auth/status", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"hosted":true`) {
		t.Fatalf("hosted login metadata: %d %s", w.Code, w.Body.String())
	}
}

func TestSharedConnectionWatchdogRevokesChangedGrant(t *testing.T) {
	f := newHostedFixture(t)
	f.server.Config.BackendMode = tenancy.BackendConnected
	tenant := f.tenant(t, "connected", "owner-a")
	token, _, err := f.catalog.CreateInstanceEnrollment(context.Background(), "owner-a", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, _, err := f.catalog.EnrollInstance(context.Background(), token, "installation", "", false)
	if err != nil {
		t.Fatal(err)
	}
	cancelled := false
	watch := &instanceConnectionWatch{key: connectionKey(grant), credential: grant.CredentialHash, cancel: func() { cancelled = true }}
	f.server.connectionMu.Lock()
	f.server.connections[watch] = struct{}{}
	f.server.connectionMu.Unlock()
	defer func() {
		f.server.connectionMu.Lock()
		delete(f.server.connections, watch)
		f.server.connectionMu.Unlock()
	}()
	f.server.checkInstanceConnections()
	if cancelled {
		t.Fatal("active grant cancelled")
	}
	if _, err = f.catalog.RevokeInstance(context.Background(), "owner-a", tenant.ID); err != nil {
		t.Fatal(err)
	}
	f.server.checkInstanceConnections()
	if !cancelled {
		t.Fatal("changed grant remained live")
	}
}

type pausedInstanceUpgrade struct {
	http.ResponseWriter
	reached chan struct{}
	release <-chan struct{}
}

func (w *pausedInstanceUpgrade) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.reached)
	<-w.release
	return w.ResponseWriter.(http.Hijacker).Hijack()
}

func TestDisconnectRejectsSocketPausedDuringUpgrade(t *testing.T) {
	testInstanceRemovalDuringUpgrade(t, false)
}
func TestDeleteRejectsSocketPausedDuringUpgrade(t *testing.T) {
	testInstanceRemovalDuringUpgrade(t, true)
}
func testInstanceRemovalDuringUpgrade(t *testing.T, deleting bool) {
	f := newHostedFixture(t)
	f.server.Config.BackendMode = tenancy.BackendConnected
	tenant := f.tenant(t, "connected", "owner-a")
	ctx := context.Background()
	enrollment, _, err := f.catalog.CreateInstanceEnrollment(ctx, "owner-a", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, credential, err := f.catalog.EnrollInstance(ctx, enrollment, "installation", "", false)
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = f.server.Config.RootDomain
		defer close(finished)
		f.server.ServeHTTP(&pausedInstanceUpgrade{ResponseWriter: w, reached: reached, release: release}, r)
	}))
	defer func() { unblock(); server.Close() }()
	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/instance-connections/ws", f.server.Config.Origin())
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Authorization", "Bearer "+credential)
	config.Protocol = []string{"doodship-instance-v1"}
	config.Dialer = &net.Dialer{Timeout: 5 * time.Second}
	type dialResult struct {
		conn *websocket.Conn
		err  error
	}
	dialed := make(chan dialResult, 1)
	go func() { conn, err := websocket.DialConfig(config); dialed <- dialResult{conn, err} }()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		unblock()
		t.Fatal("socket did not reach upgrade")
	}
	owner := f.session(t, "owner-a", tenancy.AudiencePlatform)
	path := "/api/v1/tenants/" + tenant.ID + "/connection"
	var body any
	if deleting {
		path = "/api/v1/tenants/" + tenant.ID
		body = map[string]string{"slug": tenant.Slug}
	}
	w := f.request(t, f.server.Config.RootDomain, "DELETE", path, owner, body)
	if w.Code != 204 {
		unblock()
		t.Fatal(w.Code, w.Body.String())
	}
	unblock()
	select {
	case result := <-dialed:
		if result.conn != nil {
			defer result.conn.Close()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("socket upgrade did not return")
	}
	// Keep the client open until the server returns. Closing it first would hide
	// the stale socket by making the broker exit on a client-side EOF.
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("revoked socket remained alive until the watchdog")
	}
	if f.server.Bridge.Connected(connectionKey(grant)) {
		t.Fatal("revoked generation installed a socket")
	}
}

func TestConnectedAddressLimitReturnsActionableConflict(t *testing.T) {
	w := httptest.NewRecorder()
	catalogError(w, fmt.Errorf("reserve address: %w", tenancy.ErrWorkloadHostLimit))
	if w.Code != http.StatusConflict {
		t.Fatalf("status %d", w.Code)
	}
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	want := "This instance has reached its active address limit. Remove unused application routes before publishing another."
	if body.Detail != want {
		t.Fatalf("unhelpful address limit error: %q", body.Detail)
	}
}

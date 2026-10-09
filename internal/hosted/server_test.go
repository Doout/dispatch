package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/tenancy"
)

type hostedFixture struct {
	server  *Server
	catalog *tenancy.Catalog
	opens   atomic.Int32
}

func newHostedFixture(t *testing.T) *hostedFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	catalog, err := tenancy.Open(ctx, filepath.Join(root, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { catalog.Close() })
	if err = catalog.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, user := range []tenancy.User{{ID: "platform", Name: "Platform", Email: "platform@example.test", PlatformAdmin: true, EmailVerified: true}, {ID: "owner-a", Name: "Owner A", Email: "owner-a@example.test", EmailVerified: true}, {ID: "owner-b", Name: "Owner B", Email: "owner-b@example.test", EmailVerified: true}, {ID: "member", Name: "Member", Email: "member@example.test", EmailVerified: true}} {
		if err = catalog.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	factory, err := tenancy.NewFactory(tenancy.FactoryConfig{RootDir: filepath.Join(root, "tenants")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { factory.Close() })
	f := &hostedFixture{catalog: catalog}
	cfg := Config{RootDomain: "dispatch.example.test", DataDirectory: root, ConsoleAddresses: []string{"192.0.2.10"}, WorkloadGateway: "192.0.2.11", Nameservers: []string{"ns1.example.net", "ns2.example.net"}, DNSReadToken: strings.Repeat("d", 40)}
	f.server, err = New(ctx, cfg, catalog, func(ctx context.Context, tenant tenancy.Tenant, auth *api.HostedAuth) (*TenantRuntime, error) {
		runtime, err := factory.Open(ctx, tenant.ID)
		if err != nil {
			return nil, err
		}
		f.opens.Add(1)
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, err := auth.Authenticate(r)
			if err != nil {
				problem(w, http.StatusUnauthorized, "Access denied.")
				return
			}
			respond(w, http.StatusOK, map[string]string{"tenantId": tenant.ID, "actorId": actor.ID})
		})
		return &TenantRuntime{Store: runtime.Store, Handler: handler}, nil
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.server.Close)
	return f
}
func (f *hostedFixture) session(t *testing.T, user, audience string) string {
	t.Helper()
	token, err := f.catalog.CreateSession(context.Background(), user, audience, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return token
}
func (f *hostedFixture) request(t *testing.T, host, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "https://"+host+path, bytes.NewReader(raw))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}
func (f *hostedFixture) tenant(t *testing.T, slug, owner string) tenancy.Tenant {
	t.Helper()
	token := f.session(t, "platform", tenancy.AudiencePlatform)
	w := f.request(t, f.server.Config.RootDomain, http.MethodPost, "/api/v1/platform/tenants", token, map[string]string{"slug": slug, "name": slug, "ownerEmail": owner + "@example.test"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("create tenant: %d %s", w.Code, w.Body.String())
	}
	var tenant tenancy.Tenant
	if err := json.Unmarshal(w.Body.Bytes(), &tenant); err != nil {
		t.Fatal(err)
	}
	if err := f.catalog.SetTenantState(context.Background(), tenant.ID, tenancy.StateActive); err != nil {
		t.Fatal(err)
	}
	tenant.State = tenancy.StateActive
	return tenant
}

func TestHostedPlatformMetadataDoesNotGrantTenantAuthority(t *testing.T) {
	f := newHostedFixture(t)
	a := f.tenant(t, "alpha", "owner-a")
	b := f.tenant(t, "bravo", "owner-b")
	admin := f.session(t, "platform", tenancy.AudiencePlatform)
	for _, path := range []string{"/api/v1/platform/tenants", "/api/v1/platform/tenants/" + a.ID, "/api/v1/platform/tenants/" + a.ID + "/usage?days=30"} {
		w := f.request(t, f.server.Config.RootDomain, http.MethodGet, path, admin, nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		for _, private := range []string{"owner-a@example.test", "passwordHash", "repository", "secret", "memberships"} {
			if strings.Contains(w.Body.String(), private) {
				t.Fatal("platform response contains tenant detail", private)
			}
		}
	}
	if f.opens.Load() != 0 {
		t.Fatal("metadata request opened operational database")
	}
	for _, tenant := range []tenancy.Tenant{a, b} {
		w := f.request(t, tenant.Slug+"."+f.server.Config.RootDomain, http.MethodGet, "/api/v1/private", admin, nil)
		if w.Code != 401 {
			t.Fatal("platform session accessed tenant", w.Code)
		}
	}
	w := f.request(t, f.server.Config.RootDomain, http.MethodPut, "/api/v1/tenants/"+a.ID+"/members/platform", admin, map[string]string{"role": "owner", "state": "active"})
	if w.Code != 403 {
		t.Fatal("platform enrolled itself", w.Code, w.Body.String())
	}
	if members, err := f.catalog.ListMemberships(context.Background(), "platform"); err != nil || len(members) != 0 {
		t.Fatal("creator membership", members, err)
	}
	for _, path := range []string{"/api/v1/platform/tenants/" + a.ID, "/api/v1/platform/tenants/" + a.ID + "/suspend", "/api/v1/platform/tenants/" + a.ID + "/quota"} {
		w = f.request(t, f.server.Config.RootDomain, http.MethodDelete, path, admin, nil)
		if w.Code != 404 && w.Code != 405 {
			t.Fatal("platform control route exists", path, w.Code)
		}
	}
	aToken := f.session(t, "owner-a", tenancy.TenantAudience(a.ID))
	if w = f.request(t, "alpha."+f.server.Config.RootDomain, http.MethodGet, "/api/v1/private", aToken, nil); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = f.request(t, "bravo."+f.server.Config.RootDomain, http.MethodGet, "/api/v1/private", aToken, nil); w.Code != 401 {
		t.Fatal("tenant session crossed host", w.Code)
	}
	ownerCentral := f.session(t, "owner-a", tenancy.AudiencePlatform)
	if w = f.request(t, f.server.Config.RootDomain, http.MethodGet, "/api/v1/platform/tenants", ownerCentral, nil); w.Code != 403 {
		t.Fatal("tenant owner acquired platform role", w.Code)
	}
}

func TestHostedHostOriginAndDNSBoundaries(t *testing.T) {
	f := newHostedFixture(t)
	a := f.tenant(t, "alpha", "owner-a")
	b := f.tenant(t, "bravo", "owner-b")
	token := f.session(t, "owner-a", tenancy.TenantAudience(a.ID))
	host := "alpha." + f.server.Config.RootDomain
	for _, bad := range []string{"evil.test", "preview.alpha." + f.server.Config.RootDomain, "missing." + f.server.Config.RootDomain} {
		if w := f.request(t, bad, http.MethodGet, "/api/v1/private", token, nil); w.Code != 404 {
			t.Fatal("unregistered host routed", bad, w.Code)
		}
	}
	r := httptest.NewRequest(http.MethodGet, "https://evil.test/api/v1/private", nil)
	r.Header.Set("X-Forwarded-Host", host)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("forwarded host selected tenant", w.Code)
	}
	for _, origin := range []string{"https://bravo." + f.server.Config.RootDomain, "https://preview." + host, "https://evil.test", ""} {
		r = httptest.NewRequest(http.MethodPost, "https://"+host+"/api/v1/hosted/auth/logout", nil)
		r.AddCookie(&http.Cookie{Name: tenantCookie, Value: token})
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w = httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("cookie write accepted foreign/missing origin", origin, w.Code)
		}
	}
	record := map[string]any{"name": "preview." + host, "type": "A", "values": []string{"192.0.2.20"}, "ttl": 300}
	w = f.request(t, host, http.MethodPut, "/api/v1/hosted/dns", token, record)
	if w.Code != 200 {
		t.Fatal("tenant DNS creation", w.Code, w.Body.String())
	}
	var saved tenancy.ZoneRecord
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	record["name"] = "preview.bravo." + f.server.Config.RootDomain
	w = f.request(t, host, http.MethodPut, "/api/v1/hosted/dns", token, record)
	if w.Code != 400 {
		t.Fatal("tenant wrote sibling DNS", w.Code)
	}
	record["name"] = "_acme-challenge." + host
	w = f.request(t, host, http.MethodPut, "/api/v1/hosted/dns", token, record)
	if w.Code != 400 {
		t.Fatal("tenant wrote ACME DNS", w.Code)
	}
	bToken := f.session(t, "owner-b", tenancy.TenantAudience(b.ID))
	w = f.request(t, "bravo."+f.server.Config.RootDomain, http.MethodGet, "/api/v1/hosted/dns", bToken, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), saved.ID) {
		t.Fatal("tenant saw sibling DNS", w.Code, w.Body.String())
	}
	admin := f.session(t, "platform", tenancy.AudiencePlatform)
	for _, bearer := range []string{admin, token} {
		w = f.request(t, f.server.Config.RootDomain, http.MethodGet, "/api/v1/internal/dns/snapshot", bearer, nil)
		if w.Code != 401 && w.Code != 403 {
			t.Fatal("user token read full DNS feed", w.Code)
		}
	}
	w = f.request(t, f.server.Config.RootDomain, http.MethodGet, "/api/v1/internal/dns/snapshot", f.server.Config.DNSReadToken, nil)
	if w.Code != 200 {
		t.Fatal("DNS service token rejected", w.Code, w.Body.String())
	}
}

func TestHostedPKCEExchangeAndMembershipRevocation(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	ctx := context.Background()
	if err := f.catalog.SetMembership(ctx, "owner-a", tenancy.Membership{TenantID: tenant.ID, UserID: "member", Role: tenancy.RoleMember}); err != nil {
		t.Fatal(err)
	}
	host := "alpha." + f.server.Config.RootDomain
	start := f.request(t, host, http.MethodGet, "/api/v1/hosted/auth/start", "", nil)
	if start.Code != 303 {
		t.Fatal(start.Code)
	}
	redirect, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var verifier *http.Cookie
	for _, cookie := range start.Result().Cookies() {
		if cookie.Name == "__Host-dispatch-handoff" {
			verifier = cookie
		}
	}
	if verifier == nil || !verifier.Secure || !verifier.HttpOnly || verifier.Domain != "" || verifier.Path != "/" {
		t.Fatal("handoff cookie not host-only and protected")
	}
	central := f.session(t, "member", tenancy.AudiencePlatform)
	handoff := f.request(t, f.server.Config.RootDomain, http.MethodPost, "/api/v1/account/handoffs", central, map[string]string{"tenantId": tenant.ID, "codeChallenge": redirect.Query().Get("challenge")})
	if handoff.Code != 200 {
		t.Fatal(handoff.Code, handoff.Body.String())
	}
	var payload map[string]string
	if err = json.Unmarshal(handoff.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(payload["url"])
	if err != nil {
		t.Fatal(err)
	}
	fragment, err := url.ParseQuery(target.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": fragment.Get("tenant_code")})
	exchange := func(cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "https://"+host+"/api/v1/hosted/auth/exchange", bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://"+host)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		f.server.ServeHTTP(w, r)
		return w
	}
	if w := exchange(nil); w.Code != 403 {
		t.Fatal("handoff redeemed without browser verifier", w.Code)
	}
	w := exchange(verifier)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == tenantCookie {
			session = cookie
		}
	}
	if session == nil || session.Domain != "" || !session.HttpOnly || !session.Secure {
		t.Fatal("missing protected tenant session")
	}
	if replay := exchange(verifier); replay.Code != 403 {
		t.Fatal("handoff replay", replay.Code)
	}
	w = f.request(t, host, http.MethodGet, "/api/v1/private", session.Value, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	ownerCentral := f.session(t, "owner-a", tenancy.AudiencePlatform)
	w = f.request(t, f.server.Config.RootDomain, http.MethodDelete, "/api/v1/tenants/"+tenant.ID+"/members/member", ownerCentral, nil)
	if w.Code != 204 {
		t.Fatal("revoke membership", w.Code, w.Body.String())
	}
	w = f.request(t, host, http.MethodGet, "/api/v1/private", session.Value, nil)
	if w.Code != 401 {
		t.Fatal("revoked member retained session", w.Code)
	}
}

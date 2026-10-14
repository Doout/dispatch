package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/tenancy"
)

type fakeWorkOS struct {
	deletionError     error
	admissionError    error
	organizationError error
	organization      string
	identity          workOSIdentity
	revoked           bool
	exchanges         int
	organizations     map[string]string
	members           map[string]map[string]bool
}

func (f *fakeWorkOS) AuthorizationURL(callback, state, challenge, org string) string {
	f.organization = org
	return "https://api.workos.com/user_management/authorize?" + url.Values{"redirect_uri": {callback}, "state": {state}, "code_challenge": {challenge}, "organization_id": {org}}.Encode()
}
func (f *fakeWorkOS) Exchange(context.Context, string, string) (workOSIdentity, error) {
	f.exchanges++
	identity := f.identity
	if identity.OrganizationID == "" {
		identity.OrganizationID = f.organization
	}
	return identity, nil
}
func (f *fakeWorkOS) SessionActive(context.Context, string, string) error {
	if f.revoked {
		return tenancy.ErrExpired
	}
	return nil
}
func (f *fakeWorkOS) RevokeSession(context.Context, string) error { f.revoked = true; return nil }
func (f *fakeWorkOS) EnsureOrganization(_ context.Context, external, name string) (string, error) {
	if f.organizationError != nil {
		return "", f.organizationError
	}
	if f.organizations == nil {
		f.organizations = map[string]string{}
	}
	if f.organizations[external] == "" {
		f.organizations[external] = "org_" + name
	}
	return f.organizations[external], nil
}
func (f *fakeWorkOS) AdmissionAllowed(context.Context, workOSIdentity) error { return f.admissionError }
func (f *fakeWorkOS) DeleteOrganization(_ context.Context, id, external string) error {
	if f.deletionError != nil {
		return f.deletionError
	}
	for key, organization := range f.organizations {
		if organization == id || key == external {
			delete(f.organizations, key)
			delete(f.members, organization)
		}
	}
	return nil
}
func (f *fakeWorkOS) SyncMembers(_ context.Context, org string, members map[string]bool) error {
	if f.members == nil {
		f.members = map[string]map[string]bool{}
	}
	f.members[org] = members
	return nil
}

func workOSFixture(t *testing.T) (*hostedFixture, *fakeWorkOS) {
	f := newHostedFixture(t)
	remote := &fakeWorkOS{identity: workOSIdentity{Subject: "user_new", Email: "new@example.test", Name: "New user", SessionID: "session_new"}}
	f.server.workos = remote
	return f, remote
}
func startWorkOSRequest(t *testing.T, f *hostedFixture, token string, body any) (string, *http.Cookie) {
	t.Helper()
	w := f.request(t, f.server.Config.RootDomain, http.MethodPost, "/api/v1/account/workos/start", token, body)
	if w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result["authorizationUrl"])
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("code_challenge") == "" {
		t.Fatal("missing PKCE")
	}
	return u.Query().Get("state"), w.Result().Cookies()[0]
}
func workOSCallback(f *hostedFixture, state string, browser *http.Cookie, session string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", f.server.Config.Origin()+"/api/v1/account/workos/callback?state="+url.QueryEscape(state)+"&code=code", nil)
	if browser != nil {
		r.AddCookie(browser)
	}
	if session != "" {
		r.Header.Set("Authorization", "Bearer "+session)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}
func callbackSession(w *httptest.ResponseRecorder) string {
	for _, c := range w.Result().Cookies() {
		if c.Name == platformCookie {
			return c.Value
		}
	}
	return ""
}

func TestWorkOSLoginRejectsForeignBrowserAndReplay(t *testing.T) {
	f, remote := workOSFixture(t)
	state, cookie := startWorkOSRequest(t, f, "", map[string]any{})
	if w := workOSCallback(f, state, &http.Cookie{Name: cookie.Name, Value: "different"}, ""); callbackSession(w) != "" || remote.exchanges != 0 {
		t.Fatal("foreign browser exchanged code")
	}
	w := workOSCallback(f, state, cookie, "")
	token := callbackSession(w)
	if w.Code != 303 || token == "" {
		t.Fatalf("callback: %d %s", w.Code, w.Header().Get("Location"))
	}
	u, err := f.server.authenticateSession(context.Background(), token, tenancy.AudiencePlatform)
	if err != nil || u.PlatformAdmin || u.Email != "new@example.test" {
		t.Fatalf("wrong user: %+v %v", u, err)
	}
	if w = workOSCallback(f, state, cookie, ""); callbackSession(w) != "" || remote.exchanges != 1 {
		t.Fatal("replayed code")
	}
	f.server.workosSessionMu.Lock()
	clear(f.server.workosSessions)
	f.server.workosSessionMu.Unlock()
	remote.revoked = true
	if _, err = f.server.authenticateSession(context.Background(), token, tenancy.AudiencePlatform); err == nil {
		t.Fatal("revoked WorkOS session accepted")
	}
}

func TestWorkOSInvitationReturnStartsFreshLoginWithoutExchangingCode(t *testing.T) {
	f, remote := workOSFixture(t)
	state, cookie := startWorkOSRequest(t, f, "", map[string]any{})
	for _, browser := range []*http.Cookie{nil, cookie} {
		w := workOSCallback(f, "", browser, "")
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != f.server.Config.Origin()+"/?view=login&workos_return=1" {
			t.Fatalf("invitation return: %d %s", w.Code, w.Header().Get("Location"))
		}
		if callbackSession(w) != "" || remote.exchanges != 0 || len(w.Result().Cookies()) != 0 {
			t.Fatal("unbound code created a session or changed an existing login")
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("callback could cache or disclose its code")
		}
	}
	// A direct invitation return must not consume another login in progress.
	if w := workOSCallback(f, state, cookie, ""); callbackSession(w) == "" || remote.exchanges != 1 {
		t.Fatal("invitation return interrupted a valid login")
	}
}

func TestWorkOSCallbackWithStateStillRequiresItsBrowser(t *testing.T) {
	f, remote := workOSFixture(t)
	state, _ := startWorkOSRequest(t, f, "", map[string]any{})
	w := workOSCallback(f, state, nil, "")
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil || w.Code != http.StatusSeeOther || location.Query().Get("auth_error") != "Your sign-in session expired. Enter your email to try again." {
		t.Fatalf("missing browser: %d %s", w.Code, w.Header().Get("Location"))
	}
	if callbackSession(w) != "" || remote.exchanges != 0 || location.Query().Has("workos_return") {
		t.Fatal("missing browser bypassed login binding")
	}
}
func TestWorkOSLinkPreservesMembershipAndDoesNotMatchEmails(t *testing.T) {
	f, remote := workOSFixture(t)
	f.server.Config.WorkOS.AllowPasswordLogin = true
	tenant := f.tenant(t, "alpha", "owner-a")
	remote.identity.Email = "owner-a@example.test"
	state, cookie := startWorkOSRequest(t, f, "", map[string]any{})
	if w := workOSCallback(f, state, cookie, ""); callbackSession(w) != "" {
		t.Fatal("linked by email")
	}
	old := f.session(t, "owner-a", tenancy.AudiencePlatform)
	state, cookie = startWorkOSRequest(t, f, old, map[string]any{"link": true})
	w := workOSCallback(f, state, cookie, old)
	token := callbackSession(w)
	u, err := f.server.authenticateSession(context.Background(), token, tenancy.AudiencePlatform)
	if err != nil || u.ID != "owner-a" {
		t.Fatalf("link: %+v %v %s", u, err, w.Header().Get("Location"))
	}
	if _, err = f.catalog.Membership(context.Background(), tenant.ID, u.ID); err != nil {
		t.Fatal("lost membership", err)
	}
	if err = f.server.syncWorkOSOrganization(context.Background(), tenant); err != nil {
		t.Fatal(err)
	}
	org, err := f.catalog.WorkOSOrganization(context.Background(), tenant.ID)
	if err != nil || !remote.members[org][remote.identity.Subject] {
		t.Fatalf("organization member missing: %s %v", org, err)
	}
	if err = f.server.syncWorkOSOrganization(context.Background(), tenant); err != nil || len(remote.organizations) != 1 {
		t.Fatal("organization duplicated", err)
	}
	w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/logout", token, nil)
	if w.Code != 204 || !remote.revoked {
		t.Fatalf("logout: %d", w.Code)
	}
}
func TestWorkOSRejectsLinkAfterLocalSessionRevoked(t *testing.T) {
	f, _ := workOSFixture(t)
	f.server.Config.WorkOS.AllowPasswordLogin = true
	old := f.session(t, "owner-a", tenancy.AudiencePlatform)
	state, cookie := startWorkOSRequest(t, f, old, map[string]any{"link": true})
	if err := f.catalog.RevokeSession(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	if w := workOSCallback(f, state, cookie, old); callbackSession(w) != "" {
		t.Fatal("linked after local logout")
	}
}
func TestWorkOSDisablesLocalCredentialsAndPreservesTenantDestination(t *testing.T) {
	f, _ := workOSFixture(t)
	for _, path := range []string{"/api/v1/account/login", "/api/v1/account/register", "/api/v1/account/verify-email"} {
		w := f.request(t, f.server.Config.RootDomain, "POST", path, "", map[string]string{})
		if w.Code != 403 {
			t.Fatalf("local credential path allowed: %s %d", path, w.Code)
		}
	}
	f.server.Config.WorkOS.AllowPasswordLogin = true
	tenant := f.tenant(t, "alpha", "owner-a")
	f.server.Config.WorkOS.AllowPasswordLogin = false
	state, cookie := startWorkOSRequest(t, f, "", map[string]any{"tenantId": tenant.ID, "codeChallenge": strings.Repeat("A", 43)})
	w := workOSCallback(f, state, cookie, "")
	if !strings.Contains(w.Header().Get("Location"), "tenant="+tenant.ID) {
		t.Fatalf("lost destination: %s", w.Header().Get("Location"))
	}
	u, err := f.catalog.ExternalUser(context.Background(), "workos", "user_new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.Membership(context.Background(), tenant.ID, u.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("login granted membership", err)
	}
	old := f.session(t, "member", tenancy.AudiencePlatform)
	if _, err := f.server.authenticateSession(context.Background(), old, tenancy.AudiencePlatform); err == nil {
		t.Fatal("unmanaged session accepted")
	}
}
func TestWorkOSConfigurationRequiresBothCredentials(t *testing.T) {
	for _, cfg := range []WorkOSConfig{{ClientID: "client"}, {APIKey: "secret"}, {AllowPasswordLogin: true}} {
		if cfg.validate() == nil {
			t.Fatal("incomplete config accepted")
		}
	}
	if err := (WorkOSConfig{ClientID: "client", APIKey: "secret"}).validate(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkOSOrganizationSupportsMultipleMembersAndRemoval(t *testing.T) {
	f, remote := workOSFixture(t)
	f.server.Config.WorkOS.AllowPasswordLogin = true
	tenant := f.tenant(t, "team", "owner-a")
	ctx := context.Background()
	for _, id := range []string{"owner-a", "owner-b", "member"} {
		if _, err := f.catalog.ResolveExternalUser(ctx, tenancy.ExternalLogin{Provider: "workos", Subject: "workos-" + id, Email: id + "@example.test", Name: id, LinkUserID: id, LinkVersion: 1}); err != nil {
			t.Fatal(err)
		}
		if id != "owner-a" {
			if err := f.catalog.SetMembership(ctx, "owner-a", tenancy.Membership{TenantID: tenant.ID, UserID: id, Role: tenancy.RoleMember, State: tenancy.StateActive}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := f.server.syncWorkOSOrganization(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	org, err := f.catalog.WorkOSOrganization(ctx, tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remote.members[org]) != 3 {
		t.Fatalf("expected three managed members: %+v", remote.members[org])
	}
	if err = f.catalog.DeleteMembership(ctx, "owner-a", tenant.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err = f.server.syncWorkOSOrganization(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	if remote.members[org]["workos-member"] {
		t.Fatal("removed member remains active upstream")
	}
}

func TestWorkOSCallbackRejectsWrongOrganization(t *testing.T) {
	f, remote := workOSFixture(t)
	f.server.Config.WorkOS.AllowPasswordLogin = true
	tenant := f.tenant(t, "alpha", "owner-a")
	remote.identity.OrganizationID = "org_other"
	state, cookie := startWorkOSRequest(t, f, "", map[string]any{"tenantId": tenant.ID, "codeChallenge": strings.Repeat("A", 43)})
	w := workOSCallback(f, state, cookie, "")
	if callbackSession(w) != "" || !strings.Contains(w.Header().Get("Location"), "auth_error=") {
		t.Fatal("wrong organization accepted")
	}
}

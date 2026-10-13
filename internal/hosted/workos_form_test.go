package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/tenancy"
	"golang.org/x/crypto/bcrypt"
)

type fakeWorkOSForm struct {
	*fakeWorkOS
	authenticate                            func(managedAuthentication, managedCredentials) (managedResult, error)
	sent, authenticated, waitlisted, resets int
	invitationEmail                         string
	emailAllowed                            bool
	mailError                               error
}

func managedMigrationFixture(t *testing.T) (*hostedFixture, *fakeWorkOSForm, tenancy.Tenant, managedLoginResponse, *http.Cookie) {
	t.Helper()
	f, remote := managedFixture(t)
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("previous-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	u := tenancy.User{ID: "legacy-admin", Email: "legacy-admin@example.test", Name: "Legacy", EmailVerified: true, PlatformAdmin: true, PasswordHash: string(hash)}
	if err := f.catalog.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	tenant := f.tenant(t, "legacy-team", u.ID)
	remote.identity.Email = u.Email
	// Existing accounts can migrate without admitting a new uninvited account.
	remote.admissionError = tenancy.ErrDenied
	p, cookie := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "email-code", Email: u.Email}))
	if remote.sent != 1 {
		t.Fatal("existing account did not receive a code")
	}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "123456"})
	p, cookie = managedProgress(t, w)
	if p.Step != "link-account" || callbackSession(w) != "" || strings.Contains(w.Body.String(), remote.identity.SessionID) || strings.Contains(w.Body.String(), remote.identity.Subject) {
		t.Fatal("migration skipped password proof or exposed provider identity", w.Body.String())
	}
	if _, err := f.catalog.ExternalSubject(ctx, "workos", u.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("email code linked account", err)
	}
	return f, remote, tenant, p, cookie
}

func TestManagedMigrationConfirmsPasswordOnceAndPreservesAdminAndTenants(t *testing.T) {
	f, remote, tenant, p, cookie := managedMigrationFixture(t)
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, PreviousPassword: "wrong-password"})
	p, cookie = managedProgress(t, w)
	if p.Step != "link-account" || p.Message == "" || callbackSession(w) != "" {
		t.Fatal("incorrect password completed migration")
	}
	old, oldCookie := p, cookie
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Email: "platform@example.test", PreviousPassword: "previous-password"})
	result, _ := managedProgress(t, w)
	if result.Step != "complete" || remote.authenticated != 1 {
		t.Fatal("migration required another provider code")
	}
	u, err := f.server.authenticateSession(context.Background(), callbackSession(w), tenancy.AudiencePlatform)
	if err != nil || u.ID != "legacy-admin" || !u.PlatformAdmin {
		t.Fatal("migration lost account or role", u, err)
	}
	if _, err := f.catalog.Membership(context.Background(), tenant.ID, u.ID); err != nil {
		t.Fatal("migration lost tenant", err)
	}
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", oldCookie, managedLoginInput{State: old.State, PreviousPassword: "previous-password"})
	if w.Code != 401 {
		t.Fatal("password proof replay succeeded")
	}
	// Subsequent WorkOS logins use the linked identity without local passwords.
	p, cookie = managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "email-code", Email: remote.identity.Email}))
	p, _ = managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "123456"}))
	if p.Step != "complete" {
		t.Fatal("linked login still requests a previous password")
	}
}

func TestManagedMigrationRejectsRevocationAndChangedAccounts(t *testing.T) {
	for _, change := range []string{"browser", "provider-revoked", "password-changed", "disabled", "migration-disabled"} {
		t.Run(change, func(t *testing.T) {
			f, remote, tenant, p, cookie := managedMigrationFixture(t)
			ctx := context.Background()
			switch change {
			case "browser":
				cookie = &http.Cookie{Name: managedBrowserCookie, Value: "another-browser"}
			case "provider-revoked":
				remote.revoked = true
			case "password-changed":
				if err := f.catalog.UpdateOwnPassword(ctx, "legacy-admin", "previous-password", "replacement-password"); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				if err := f.catalog.SetMembership(ctx, "legacy-admin", tenancy.Membership{TenantID: tenant.ID, UserID: "owner-a", Role: tenancy.RoleOwner}); err != nil {
					t.Fatal(err)
				}
				if err := f.catalog.SetUserState(ctx, "legacy-admin", tenancy.StateDisabled); err != nil {
					t.Fatal(err)
				}
			case "migration-disabled":
				f.server.Config.WorkOS.AllowPasswordLogin = false
			}
			w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, PreviousPassword: "previous-password"})
			if w.Code < 400 || callbackSession(w) != "" {
				t.Fatal("invalid migration succeeded", w.Code)
			}
			if _, err := f.catalog.ExternalSubject(ctx, "workos", "legacy-admin"); !errors.Is(err, tenancy.ErrNotFound) {
				t.Fatal("invalid migration linked identity", err)
			}
		})
	}
}

func (f *fakeWorkOSForm) Authenticate(_ context.Context, state managedAuthentication, in managedCredentials) (managedResult, error) {
	f.authenticated++
	if f.authenticate != nil {
		return f.authenticate(state, in)
	}
	return managedResult{Identity: &f.identity}, nil
}
func (f *fakeWorkOSForm) SendCode(context.Context, string, string, string, string, bool) error {
	f.sent++
	return f.mailError
}
func (f *fakeWorkOSForm) InvitationEmail(context.Context, string) (string, error) {
	if f.invitationEmail == "" {
		return "", tenancy.ErrDenied
	}
	return f.invitationEmail, nil
}
func (f *fakeWorkOSForm) EmailAllowed(context.Context, string) (bool, error) {
	return f.emailAllowed, nil
}
func (f *fakeWorkOSForm) RequestPasswordReset(context.Context, string) error {
	f.resets++
	return f.mailError
}
func (f *fakeWorkOSForm) ResetPassword(context.Context, string, string) error { return f.mailError }
func (f *fakeWorkOSForm) JoinWaitlist(context.Context, string) error {
	f.waitlisted++
	return f.mailError
}
func managedFixture(t *testing.T) (*hostedFixture, *fakeWorkOSForm) {
	f, remote := workOSFixture(t)
	client := &fakeWorkOSForm{fakeWorkOS: remote}
	f.server.workos = client
	f.server.Config.WorkOS.AllowPasswordLogin = true
	return f, client
}
func managedRequest(t *testing.T, f *hostedFixture, path, origin, session string, cookie *http.Cookie, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", f.server.Config.Origin()+"/api/v1/account/workos/"+path, strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", origin)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	if session != "" {
		r.Header.Set("Authorization", "Bearer "+session)
	}
	w := httptest.NewRecorder()
	f.server.ServeHTTP(w, r)
	return w
}
func managedProgress(t *testing.T, w *httptest.ResponseRecorder) (managedLoginResponse, *http.Cookie) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("response %d %s", w.Code, w.Body.String())
	}
	var p managedLoginResponse
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == managedBrowserCookie && c.MaxAge > 0 {
			cookie = c
		}
	}
	return p, cookie
}

func TestManagedLoginRequiresOriginAndDoesNotSendUninvitedMail(t *testing.T) {
	f, remote := managedFixture(t)
	in := managedLoginInput{Method: "email-code", Email: remote.identity.Email}
	for _, origin := range []string{"", "https://attacker.test"} {
		w := managedRequest(t, f, "authenticate", origin, "", nil, in)
		if w.Code != 403 {
			t.Fatalf("origin %q: %d", origin, w.Code)
		}
	}
	p, cookie := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, in))
	if remote.sent != 0 || remote.authenticated != 0 || p.Step != "email-code" || cookie == nil {
		t.Fatal("uninvited login sent mail or disclosed eligibility")
	}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "123456"})
	if w.Code != 200 || remote.authenticated != 0 || strings.Contains(w.Header().Get("Set-Cookie"), platformCookie) {
		t.Fatal("uninvited email code authenticated")
	}
}
func TestManagedInvitationCodeIsBoundSingleUseAndCannotCreateAdmin(t *testing.T) {
	f, remote := managedFixture(t)
	remote.invitationEmail = remote.identity.Email
	p, cookie := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "email-code", Email: remote.identity.Email, InvitationToken: "invite"}))
	if remote.sent != 1 {
		t.Fatal("invited code not sent")
	}
	if strings.Contains(p.State, "invite") || strings.Contains(p.State, "session") {
		t.Fatal("upstream secret exposed")
	}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", &http.Cookie{Name: managedBrowserCookie, Value: "other"}, managedLoginInput{State: p.State, Code: "123456"})
	if w.Code != 401 || remote.authenticated != 0 {
		t.Fatal("other browser authenticated")
	}
	result, _ := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "123456"}))
	if result.Step != "complete" {
		t.Fatal("invited login did not complete")
	}
	u, err := f.catalog.ExternalUser(context.Background(), "workos", remote.identity.Subject)
	if err != nil || u.PlatformAdmin {
		t.Fatal("new login elevated privileges", err)
	}
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "123456"})
	if w.Code != 401 || remote.authenticated != 1 {
		t.Fatal("login replay succeeded")
	}
}
func TestManagedVerificationRetryRotatesStateAndNeverReturnsProviderToken(t *testing.T) {
	f, remote := managedFixture(t)
	remote.authenticate = func(state managedAuthentication, in managedCredentials) (managedResult, error) {
		if state.Step == "password" {
			state.Step = "verify-email"
			state.PendingToken = "private-provider-token"
			return managedResult{Next: &state}, nil
		}
		if state.PendingToken != "private-provider-token" {
			t.Fatal("provider token lost")
		}
		if in.Code == "000000" {
			return managedResult{}, errors.New("private upstream error")
		}
		return managedResult{Identity: &remote.identity}, nil
	}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "password", Email: remote.identity.Email, Password: "password"})
	if strings.Contains(w.Body.String(), "private-provider") {
		t.Fatal("provider token returned")
	}
	p, cookie := managedProgress(t, w)
	if p.Step != "verify-email" {
		t.Fatal("missing verification")
	}
	retry, newCookie := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "000000"}))
	if retry.State == p.State || retry.Message == "" || newCookie.Value == cookie.Value {
		t.Fatal("retry did not rotate")
	}
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", newCookie, managedLoginInput{State: retry.State, Code: "123456"})
	if w.Code != 200 {
		t.Fatalf("verification: %d %s", w.Code, w.Body.String())
	}
}
func TestManagedOrganizationSelectionRestrictsIDsAndTenantHandoffStillChecksMembership(t *testing.T) {
	f, remote := managedFixture(t)
	tenant := f.tenant(t, "team", "owner-a")
	remote.identity.OrganizationID = "org_team"
	remote.authenticate = func(state managedAuthentication, in managedCredentials) (managedResult, error) {
		if state.Step == "password" {
			state.Step = "organization"
			state.PendingToken = "private"
			state.Organizations = []managedOrganization{{ID: "org_team", Name: "Team"}}
			return managedResult{Next: &state}, nil
		}
		if in.Organization != "org_team" {
			t.Fatal("arbitrary organization reached provider")
		}
		return managedResult{Identity: &remote.identity}, nil
	}
	p, cookie := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "password", Email: remote.identity.Email, Password: "password", TenantID: tenant.ID, CodeChallenge: strings.Repeat("A", 43)}))
	p, cookie = managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, OrganizationID: "org_attacker"}))
	if remote.authenticated != 1 || p.Message == "" {
		t.Fatal("invalid selection sent to provider")
	}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, OrganizationID: "org_team"})
	result, _ := managedProgress(t, w)
	if !strings.Contains(result.ReturnTo, tenant.ID) {
		t.Fatal("handoff target lost")
	}
	var session string
	for _, c := range w.Result().Cookies() {
		if c.Name == platformCookie {
			session = c.Value
		}
	}
	w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/handoffs", session, map[string]string{"tenantId": tenant.ID, "codeChallenge": strings.Repeat("A", 43)})
	if w.Code != 403 {
		t.Fatalf("non-member handoff %d", w.Code)
	}
}
func TestManagedLinkRequiresCurrentAccountAndItsEmail(t *testing.T) {
	f, remote := managedFixture(t)
	remote.identity.Email = "owner-a@example.test"
	in := managedLoginInput{Method: "email-code", Email: remote.identity.Email, Link: true}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, in)
	if w.Code != 401 {
		t.Fatal("anonymous linking allowed")
	}
	session := f.session(t, "owner-a", tenancy.AudiencePlatform)
	in.Email = "attacker@example.test"
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), session, nil, in)
	if w.Code != 400 {
		t.Fatal("wrong email linking allowed")
	}
	in.Email = remote.identity.Email
	p, cookie := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), session, nil, in))
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", cookie, managedLoginInput{State: p.State, Code: "123456"})
	if w.Code != 403 {
		t.Fatal("expired existing session linked")
	}
	p, cookie = managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), session, nil, in))
	result, _ := managedProgress(t, managedRequest(t, f, "authenticate", f.server.Config.Origin(), session, cookie, managedLoginInput{State: p.State, Code: "123456"}))
	u, err := f.catalog.ExternalUser(context.Background(), "workos", remote.identity.Subject)
	if err != nil || u.ID != "owner-a" || result.ReturnTo != "/?view=account" {
		t.Fatal("explicit identity linking failed", err)
	}
}
func TestManagedInvitationsWaitlistResetAndDisabledMethods(t *testing.T) {
	f, remote := managedFixture(t)
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "email-code", Email: remote.identity.Email, InvitationToken: "bad"})
	if w.Code != 400 || remote.sent != 0 {
		t.Fatal("invalid invitation sent email")
	}
	f.server.Config.WorkOS.LoginMethods = []string{"email-code"}
	w = managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "password", Email: remote.identity.Email, Password: "pw"})
	if w.Code != 400 {
		t.Fatal("disabled password used")
	}
	w = managedRequest(t, f, "password-reset", f.server.Config.Origin(), "", nil, map[string]string{"email": remote.identity.Email})
	if w.Code != 404 || remote.resets != 0 {
		t.Fatal("disabled reset used")
	}
	w = managedRequest(t, f, "waitlist", f.server.Config.Origin(), "", nil, map[string]string{"email": " NEW@example.test "})
	if w.Code != 202 || remote.waitlisted != 1 {
		t.Fatal("waitlist request failed")
	}
	if _, err := f.catalog.UserByEmail(context.Background(), "new@example.test"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("waitlist created local account")
	}
}
func TestManagedContinuationDeadlineCannotBeExtendedByRetries(t *testing.T) {
	f, remote := managedFixture(t)
	remote.authenticate = func(state managedAuthentication, _ managedCredentials) (managedResult, error) {
		state.Step = "verify-email"
		state.Deadline = time.Now().Add(-time.Second)
		return managedResult{Next: &state}, nil
	}
	w := managedRequest(t, f, "authenticate", f.server.Config.Origin(), "", nil, managedLoginInput{Method: "password", Email: remote.identity.Email, Password: "pw"})
	if w.Code != 401 {
		t.Fatal("expired continuation returned")
	}
}

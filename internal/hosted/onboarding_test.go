package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/doout/dispatch/internal/tenancy"
)

func TestTenantCreationCreatesWorkOSOrganizationImmediately(t *testing.T) {
	f, remote := workOSFixture(t)
	f.server.Config.WorkOS.AllowPasswordLogin = true
	token := f.session(t, "member", tenancy.AudiencePlatform)
	w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/tenants", token, map[string]string{"slug": "mine", "name": "Mine"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var tenant tenancy.Tenant
	if err := json.Unmarshal(w.Body.Bytes(), &tenant); err != nil {
		t.Fatal(err)
	}
	if id, err := f.catalog.WorkOSOrganization(context.Background(), tenant.ID); err != nil || id != "org_Mine" {
		t.Fatalf("organization wasn't created during request: %q %v", id, err)
	}
	if f.opens.Load() != 0 {
		t.Fatal("creation opened tenant workload database")
	}
	if w = f.request(t, f.server.Config.RootDomain, "GET", "/api/v1/platform/tenants", token, nil); w.Code != 403 {
		t.Fatal("creator gained platform admin")
	}
	remote.organizationError = errors.New("WorkOS outage")
	w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/tenants", token, map[string]string{"slug": "retry", "name": "Retry"})
	if w.Code != 202 {
		t.Fatalf("outage lost durable tenant: %d", w.Code)
	}
	if err := f.server.ReconcileTenants(context.Background()); err == nil {
		t.Fatal("provider outage ignored")
	}
	retryPending, err := f.catalog.TenantBySlug(context.Background(), "retry")
	if err != nil || retryPending.State != tenancy.StatePending || f.opens.Load() != 1 {
		t.Fatal("pending tenant provisioned before organization was ready")
	}
	remote.organizationError = nil
	if err := f.server.ReconcileTenants(context.Background()); err != nil {
		t.Fatal(err)
	}
	retry, err := f.catalog.TenantBySlug(context.Background(), "retry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.catalog.WorkOSOrganization(context.Background(), retry.ID); err != nil {
		t.Fatal("organization wasn't recovered", err)
	}
	if len(remote.organizations) != 2 {
		t.Fatal("retry created duplicate organization")
	}
}

func TestClosedRegistrationDoesNotDependOnSMTP(t *testing.T) {
	f := newHostedFixture(t)
	f.server.Config.SMTP.Address = "smtp.example.test:587"
	for _, path := range []string{"register", "verify-email"} {
		w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/"+path, "", map[string]string{"email": "new@example.test", "name": "New"})
		if w.Code != 403 {
			t.Fatalf("%s returned %d", path, w.Code)
		}
	}
	if _, err := f.catalog.UserByEmail(context.Background(), "new@example.test"); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("closed registration created an account")
	}
}

func TestUninvitedWorkOSLoginCannotCreateAccount(t *testing.T) {
	f, remote := workOSFixture(t)
	remote.admissionError = tenancy.ErrDenied
	state, cookie := startWorkOSRequest(t, f, "", map[string]any{})
	w := workOSCallback(f, state, cookie, "")
	if callbackSession(w) != "" {
		t.Fatal("uninvited user got a session")
	}
	if _, err := f.catalog.UserByEmail(context.Background(), remote.identity.Email); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("uninvited account created", err)
	}
	remote.admissionError = nil
	state, cookie = startWorkOSRequest(t, f, "", map[string]any{})
	if w = workOSCallback(f, state, cookie, ""); callbackSession(w) == "" {
		t.Fatal("invited user denied")
	}
	remote.admissionError = tenancy.ErrDenied
	state, cookie = startWorkOSRequest(t, f, "", map[string]any{})
	if w = workOSCallback(f, state, cookie, ""); callbackSession(w) == "" {
		t.Fatal("existing account required another invitation")
	}
}

func TestWaitlistUsesAuthKitSignupScreen(t *testing.T) {
	f, _ := workOSFixture(t)
	w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/workos/start", "", map[string]any{"waitlist": true})
	var result map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(result["authorizationUrl"])
	if err != nil || u.Query().Get("screen_hint") != "sign-up" || u.Query().Get("code_challenge") == "" {
		t.Fatal("waitlist didn't use browser-bound AuthKit flow")
	}
	w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/workos/start", "", map[string]any{"waitlist": true, "link": true})
	if w.Code != 400 {
		t.Fatal("waitlist accepted account linking")
	}
}

func TestJoinRequestRoutesKeepTenantAuthority(t *testing.T) {
	f := newHostedFixture(t)
	ctx := context.Background()
	tenant := f.tenant(t, "alpha", "owner-a")
	user := f.session(t, "member", tenancy.AudiencePlatform)
	owner := f.session(t, "owner-a", tenancy.AudiencePlatform)
	if w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/tenants", user, map[string]string{"slug": "spoof", "name": "Spoof", "ownerEmail": "owner-b@example.test"}); w.Code != 400 {
		t.Fatal("self-create accepted a different owner")
	}
	if w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/join-requests", "", map[string]string{"slug": "alpha"}); w.Code != 401 {
		t.Fatal("anonymous join accepted")
	}
	if w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/account/join-requests", user, map[string]string{"slug": "alpha"}); w.Code != 202 {
		t.Fatalf("join returned %d %s", w.Code, w.Body.String())
	}
	if _, err := f.catalog.Membership(ctx, tenant.ID, "member"); err == nil {
		t.Fatal("request acquired membership")
	}
	path := "/api/v1/tenants/" + tenant.ID + "/join-requests"
	if w := f.request(t, f.server.Config.RootDomain, "GET", path, user, nil); w.Code != 403 && w.Code != 404 {
		t.Fatal("requester could read other requests")
	}
	w := f.request(t, f.server.Config.RootDomain, "GET", path, owner, nil)
	var requests []tenancy.MemberUser
	if err := json.Unmarshal(w.Body.Bytes(), &requests); err != nil || w.Code != 200 || len(requests) != 1 {
		t.Fatalf("owner requests: %d %s", w.Code, w.Body.String())
	}
	if w = f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/tenants/"+tenant.ID+"/members", owner, map[string]string{"email": "member@example.test", "role": "member"}); w.Code != 204 {
		t.Fatalf("approval: %d %s", w.Code, w.Body.String())
	}
	if _, err := f.catalog.Membership(ctx, tenant.ID, "member"); err != nil {
		t.Fatal("approval didn't grant membership", err)
	}
}

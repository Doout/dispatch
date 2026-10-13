package hosted

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/tenancy"
)

func TestTenantProviderEnrollmentAndSessionIsolation(t *testing.T) {
	f := newHostedFixture(t)
	ctx := context.Background()
	tenant := f.tenant(t, "alpha", "owner-a")
	other := f.tenant(t, "bravo", "owner-b")
	factory := f.server.OpenRuntime
	var auth *api.HostedAuth
	f.server.OpenRuntime = func(ctx context.Context, t tenancy.Tenant, a *api.HostedAuth) (*TenantRuntime, error) {
		if t.ID == tenant.ID {
			auth = a
		}
		return factory(ctx, t, a)
	}
	runtime, err := f.server.runtime(tenant)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	provider := core.AuthProvider{ID: "github", Name: "GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.com", APIURL: "https://api.github.com", ClientID: "client", EncryptedClientSecret: "encrypted", State: core.AuthProviderStateReady, Provisioning: core.AuthProvisionApproval, CreatedAt: now, UpdatedAt: now}
	if err = runtime.Store.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	user, err := auth.EnrollExternal(ctx, provider.ID, "subject", "new-member@example.test", "New member")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", f.server.Config.TenantOrigin(tenant.Slug)+"/api/v1/auth/callback", nil)
	if err = auth.CompleteExternal(httptest.NewRecorder(), r, user, provider.ID, "subject"); err == nil {
		t.Fatal("unapproved identity got a session")
	}
	requests, err := f.catalog.TenantJoinRequests(ctx, "owner-a", tenant.ID)
	if err != nil || len(requests) != 1 || requests[0].ID != user {
		t.Fatalf("requests: %+v %v", requests, err)
	}
	if _, err = f.catalog.TenantJoinRequests(ctx, "platform", tenant.ID); err == nil {
		t.Fatal("platform admin read tenant identities")
	}
	if _, err = f.catalog.TenantJoinRequests(ctx, "owner-b", tenant.ID); err == nil {
		t.Fatal("sibling owner read tenant identities")
	}
	// A local identity record alone grants no access. The owner must approve.
	if err = runtime.Store.UpsertExternalIdentity(ctx, core.ExternalIdentity{ProviderID: provider.ID, Subject: "subject", UserID: user, Login: "new", Email: "new-member@example.test", CreatedAt: now, LastLogin: now}); err != nil {
		t.Fatal(err)
	}
	w := f.request(t, f.server.Config.RootDomain, "POST", "/api/v1/tenants/"+tenant.ID+"/members", f.session(t, "owner-a", tenancy.AudiencePlatform), map[string]string{"email": "new-member@example.test", "role": "member"})
	if w.Code != 204 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	if err = auth.CompleteExternal(w, r, user, provider.ID, "subject"); err != nil {
		t.Fatal(err)
	}
	token := w.Result().Cookies()[0].Value
	if w = f.request(t, tenant.Slug+"."+f.server.Config.RootDomain, "GET", "/api/v1/private", token, nil); w.Code != 200 {
		t.Fatalf("tenant login: %d %s", w.Code, w.Body.String())
	}
	for _, host := range []string{f.server.Config.RootDomain, other.Slug + "." + f.server.Config.RootDomain} {
		path := "/api/v1/private"
		if host == f.server.Config.RootDomain {
			path = "/api/v1/account"
		}
		if w = f.request(t, host, "GET", path, token, nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("cross-host session accepted: %s %d", host, w.Code)
		}
	}
	provider.State = core.AuthProviderStateDisabled
	if err = runtime.Store.UpdateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if w = f.request(t, tenant.Slug+"."+f.server.Config.RootDomain, "GET", "/api/v1/private", token, nil); w.Code != 401 {
		t.Fatal("disabled provider session accepted")
	}
	provider.State = core.AuthProviderStateReady
	if err = runtime.Store.UpdateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Store.DeleteExternalIdentity(ctx, provider.ID, user); err != nil {
		t.Fatal(err)
	}
	if w = f.request(t, tenant.Slug+"."+f.server.Config.RootDomain, "GET", "/api/v1/private", token, nil); w.Code != 401 {
		t.Fatal("unlinked identity session accepted")
	}
}

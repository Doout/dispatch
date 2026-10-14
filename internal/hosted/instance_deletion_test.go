package hosted

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/doout/dispatch/internal/tenancy"
)

func TestOwnerDeletesInstanceDuringWorkOSOutage(t *testing.T) {
	f, remote := workOSFixture(t)
	f.server.Config.WorkOS.AllowPasswordLogin = true
	f.server.Config.BackendMode = tenancy.BackendConnected
	instance := f.tenant(t, "example", "owner-a")
	ctx := context.Background()
	owner := f.session(t, "owner-a", tenancy.AudiencePlatform)
	path := "/api/v1/tenants/" + instance.ID
	for _, user := range []string{"platform", "owner-b", "member"} {
		w := f.request(t, f.server.Config.RootDomain, http.MethodDelete, path, f.session(t, user, tenancy.AudiencePlatform), map[string]string{"slug": instance.Slug})
		if w.Code != 403 {
			t.Fatalf("%s can delete: %d", user, w.Code)
		}
	}
	if w := f.request(t, f.server.Config.RootDomain, http.MethodDelete, path, owner, map[string]string{"slug": "wrong"}); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	remote.deletionError = errors.New("provider unavailable")
	w := f.request(t, f.server.Config.RootDomain, http.MethodDelete, path, owner, map[string]string{"slug": instance.Slug})
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = f.request(t, instance.Slug+"."+f.server.Config.RootDomain, http.MethodGet, "/", owner, nil); w.Code != 404 {
		t.Fatal("deleted address still available", w.Code)
	}
	if err := f.server.ReconcileTenants(ctx); err == nil {
		t.Fatal("provider failure ignored")
	}
	pending, err := f.catalog.PendingOrganizationDeletions(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatal("cleanup not durable", pending, err)
	}
	// A stale snapshot from another local reconcile must not recreate the org.
	if err = f.server.syncWorkOSOrganization(ctx, instance); err != nil {
		t.Fatal(err)
	}
	if len(remote.organizations) != 1 {
		t.Fatal("organization count changed before cleanup")
	}
	remote.deletionError = nil
	if err = f.server.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if len(remote.organizations) != 0 {
		t.Fatal("organization remained after retry")
	}
	pending, err = f.catalog.PendingOrganizationDeletions(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("cleanup was not acknowledged", pending, err)
	}
	if f.opens.Load() != 0 {
		t.Fatal("deletion opened customer's data")
	}
	if w = f.request(t, f.server.Config.RootDomain, http.MethodGet, "/api/v1/account", owner, nil); w.Code != 200 {
		t.Fatal("account lost access", w.Code)
	}
}

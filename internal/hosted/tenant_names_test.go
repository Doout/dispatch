package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/doout/dispatch/internal/tenancy"
)

func TestReservedTenantNames(t *testing.T) {
	f := newHostedFixture(t)
	cfg := f.server.Config
	cfg.ReservedTenantNames = []string{" Demo ", "local"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	f.server.Config = cfg
	token := f.session(t, "platform", tenancy.AudiencePlatform)
	for _, name := range []string{"p223-team", "p223-abc-team", "pr-anything", "app-anything", "local", "demo", " LOCAL ", "Demo"} {
		w := f.request(t, cfg.RootDomain, "POST", "/api/v1/platform/tenants", token, map[string]string{"slug": name, "name": name, "ownerEmail": "owner-a@example.test"})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("name %q: %d %s", name, w.Code, w.Body.String())
		}
	}
	if _, err := f.catalog.CreateTenant(context.Background(), tenancy.CreateTenantInput{Slug: "LOCAL", Name: "Local", CreatorID: "platform", InitialOwnerID: "owner-a"}); !errors.Is(err, tenancy.ErrInvalid) {
		t.Fatalf("catalog accepted local: %v", err)
	}
	// Existing tenant metadata must not make a newly reserved hostname routable.
	tenant, err := f.catalog.CreateTenant(context.Background(), tenancy.CreateTenantInput{Slug: "demo", Name: "Demo", CreatorID: "platform", InitialOwnerID: "owner-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.catalog.SetTenantState(context.Background(), tenant.ID, "active"); err != nil {
		t.Fatal(err)
	}
	w := f.request(t, "demo."+cfg.RootDomain, "GET", "/api/v1/hosted", "", nil)
	if w.Code != http.StatusNotFound || f.opens.Load() != 0 {
		t.Fatalf("reserved host routed: %d", w.Code)
	}
	w = f.request(t, cfg.RootDomain, "GET", "/api/v1/hosted", "", nil)
	var metadata struct {
		Reserved []string `json:"reservedTenantNames"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(metadata.Reserved, "local") || !slices.Contains(metadata.Reserved, "demo") {
		t.Fatalf("missing names: %v", metadata.Reserved)
	}
	cfg.ReservedTenantNames = []string{"bad.name"}
	if cfg.Validate() == nil {
		t.Fatal("invalid reserved name accepted")
	}
}

package hosted

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/doout/dispatch/internal/authoritativedns"
	"github.com/doout/dispatch/internal/tenancy"
)

func TestCatalogSolverChallengeOwnership(t *testing.T) {
	f := newHostedFixture(t)
	a := f.tenant(t, "alpha", "owner-a")
	b := f.tenant(t, "bravo", "owner-b")
	ctx := context.Background()
	solver := catalogSolver{f.server}
	challenge := authoritativedns.Challenge{ID: "order-a", OwnerID: a.ID, Generation: 1, Name: "_acme-challenge.alpha." + f.server.Config.RootDomain, Value: "proof-a"}
	for _, name := range []string{"_acme-challenge.bravo." + f.server.Config.RootDomain, "_acme-challenge." + f.server.Config.RootDomain, "_acme-challenge.foreign.test"} {
		wrong := challenge
		wrong.Name = name
		if err := solver.Present(ctx, wrong); !errors.Is(err, tenancy.ErrDenied) {
			t.Fatalf("foreign challenge accepted: %s: %v", name, err)
		}
	}
	if err := solver.Present(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	generation, _, err := f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := solver.Present(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.catalog.ZoneRecords(ctx)
	if err != nil || after != generation {
		t.Fatal("idempotent challenge publication changed the zone")
	}
	wrong := challenge
	wrong.Value = "replacement-proof"
	if err := solver.Present(ctx, wrong); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("challenge identity allowed a different value", err)
	}
	if err := solver.Cleanup(ctx, wrong); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("mismatched cleanup removed a proof", err)
	}
	wrong.OwnerID = b.ID
	if err := solver.Cleanup(ctx, wrong); !errors.Is(err, tenancy.ErrDenied) {
		t.Fatal("sibling cleanup accepted", err)
	}

	newer := challenge
	newer.Generation, newer.Value = 2, "new-proof"
	parallel := challenge
	parallel.ID, parallel.Value = "other-order", "concurrent-proof"
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for _, ch := range []authoritativedns.Challenge{newer, parallel} {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- solver.Present(ctx, ch) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := solver.Cleanup(ctx, challenge); err != nil {
		t.Fatal(err)
	}
	if err := solver.Cleanup(ctx, challenge); err != nil {
		t.Fatal("delayed cleanup was not idempotent", err)
	}
	_, records, err := f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	proofs := map[string]bool{}
	for _, record := range records {
		if record.Name == challenge.Name {
			proofs[record.Values[0]] = true
		}
	}
	if len(proofs) != 2 || !proofs[newer.Value] || !proofs[parallel.Value] {
		t.Fatal("stale cleanup affected current challenges", proofs)
	}
}

func TestHostedDNSReconcilesGatewayChangesAndRemoval(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	ctx := context.Background()
	host := tenant.Slug + "." + f.server.Config.RootDomain
	user := tenancy.ZoneRecord{ID: "custom", OwnerID: "records:" + tenant.ID, TenantID: tenant.ID, Name: "custom." + host, Type: "A", Values: []string{"192.0.2.42"}, TTL: 300}
	if _, err := f.catalog.SaveZoneRecord(ctx, user); err != nil {
		t.Fatal(err)
	}
	replica, err := authoritativedns.NewServer(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gateway := range []string{"gateway.example.net", "192.0.2.11", "2001:db8::11", "new-gateway.example.net", ""} {
		f.server.Config.WorkloadGateway = gateway
		if err := f.server.prepareTenantDNS(ctx, tenant); err != nil {
			t.Fatalf("gateway %q: %v", gateway, err)
		}
		snapshot, err := f.server.dnsSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := replica.Apply(snapshot); err != nil {
			t.Fatal("catalog published an invalid DNS zone", err)
		}
		count, custom := 0, false
		for _, record := range snapshot.Records {
			if record.Name == "*."+host {
				count++
				if len(record.Values) != 1 || record.Values[0] != gateway {
					t.Fatal("obsolete wildcard address remains")
				}
			}
			if record.ID == user.ID {
				custom = true
			}
		}
		if !custom || gateway == "" && count != 0 || gateway != "" && count != 1 {
			t.Fatal("gateway reconciliation removed the wrong records")
		}
	}
	domain, err := f.catalog.DomainByHostname(ctx, "*."+host)
	if err != nil || domain.State != tenancy.StateDisabled {
		t.Fatal("removed gateway remained active", domain, err)
	}
}

func TestHostedDNSRootIsImmutableAndOldGlueIsRemoved(t *testing.T) {
	f := newHostedFixture(t)
	ctx := context.Background()
	ns := "authority." + f.server.Config.RootDomain
	f.server.Config.Nameservers = []string{ns, "ns2.example.net"}
	f.server.Config.NameserverAddresses = map[string][]string{ns: {"192.0.2.53"}}
	if err := f.server.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	generation, _, err := f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.catalog.ZoneRecords(ctx)
	if err != nil || generation != after {
		t.Fatal("unchanged DNS configuration advanced generation")
	}
	f.server.Config.Nameservers = []string{"ns1.example.net", "ns2.example.net"}
	if err := f.server.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	_, records, err := f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Name == ns {
			t.Fatal("former nameserver glue remained published")
		}
	}
	f.server.Config.RootDomain = "other.example.test"
	if err := f.server.Prepare(ctx); err == nil {
		t.Fatal("catalog accepted another root domain")
	}
	_, records, err = f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if strings.HasSuffix(record.Name, "other.example.test") {
			t.Fatal("failed root change partially altered the zone")
		}
	}
}

func TestHostedNameserversReserveTheirTenantSubtree(t *testing.T) {
	for _, prefix := range []string{"alpha", "authority.alpha", "authority.region.alpha"} {
		t.Run(prefix, func(t *testing.T) {
			f := newHostedFixture(t)
			root := f.server.Config.RootDomain
			ns := prefix + "." + root
			f.server.Config.Nameservers = []string{ns, "ns2.example.net"}
			f.server.Config.NameserverAddresses = map[string][]string{ns: {"192.0.2.53"}}
			if err := f.server.Config.Validate(); err != nil {
				t.Fatal(err)
			}
			if err := f.server.Prepare(t.Context()); err != nil {
				t.Fatal(err)
			}
			token := f.session(t, "platform", tenancy.AudiencePlatform)
			w := f.request(t, root, http.MethodPost, "/api/v1/platform/tenants", token, map[string]string{"slug": "alpha", "name": "Alpha", "ownerEmail": "owner-a@example.test"})
			if w.Code != http.StatusConflict {
				t.Fatalf("tenant took a nameserver's subtree: %d %s", w.Code, w.Body.String())
			}
			if _, err := f.catalog.TenantBySlug(t.Context(), "alpha"); !errors.Is(err, tenancy.ErrNotFound) {
				t.Fatal("reserved tenant was created", err)
			}
			// A label that merely starts with the reserved slug remains usable.
			f.tenant(t, "alphax", "owner-a")
		})
	}
	t.Run("external nameserver", func(t *testing.T) {
		f := newHostedFixture(t)
		f.server.Config.Nameservers = []string{"authority.alpha.example.net", "ns2.example.net"}
		f.tenant(t, "alpha", "owner-a")
	})
}

func TestHostedDNSCannotMoveNameserversIntoExistingTenant(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	if err := f.server.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.server.prepareTenantDNS(t.Context(), tenant); err != nil {
		t.Fatal(err)
	}
	generation, records, err := f.catalog.ZoneRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"alpha", "authority.alpha", "authority.region.alpha"} {
		ns := prefix + "." + f.server.Config.RootDomain
		f.server.Config.Nameservers = []string{ns, "ns2.example.net"}
		f.server.Config.NameserverAddresses = map[string][]string{ns: {"192.0.2.53"}}
		if err := f.server.Config.Validate(); err != nil {
			t.Fatal(err)
		}
		if err := f.server.Prepare(t.Context()); err == nil {
			t.Fatal("nameserver reconfiguration took an existing tenant's domain", ns)
		}
		afterGeneration, afterRecords, err := f.catalog.ZoneRecords(t.Context())
		if err != nil || generation != afterGeneration || !reflect.DeepEqual(records, afterRecords) {
			t.Fatal("rejected nameserver reconfiguration changed DNS records", err)
		}
	}
}

func TestHostedTenantCannotChangePlatformOwnedDNSName(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	host := "alpha." + f.server.Config.RootDomain
	name := "authority." + host
	// Defense for existing catalog state: even a platform-owned record below
	// a tenant's domain must not gain tenant-controlled values or record types.
	_, err := f.catalog.SaveZoneRecord(t.Context(), tenancy.ZoneRecord{ID: "platform-glue", OwnerID: platformOwner, TenantID: platformOwner, Name: name, Type: "A", Values: []string{"192.0.2.53"}, TTL: 300})
	if err != nil {
		t.Fatal(err)
	}
	generation, records, err := f.catalog.ZoneRecords(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	token := f.session(t, "owner-a", tenancy.TenantAudience(tenant.ID))
	for _, method := range []string{http.MethodPut, http.MethodDelete} {
		for kind, values := range map[string][]string{"A": {"192.0.2.99"}, "AAAA": {"2001:db8::99"}, "TXT": {"tenant-value"}} {
			w := f.request(t, host, method, "/api/v1/hosted/dns", token, map[string]any{"name": name, "type": kind, "values": values, "ttl": 300})
			if w.Code != http.StatusConflict {
				t.Fatalf("tenant changed a platform-owned name: %s %s: %d %s", method, kind, w.Code, w.Body.String())
			}
		}
	}
	afterGeneration, afterRecords, err := f.catalog.ZoneRecords(t.Context())
	if err != nil || generation != afterGeneration || !reflect.DeepEqual(records, afterRecords) {
		t.Fatal("tenant writes changed platform records", err)
	}
	w := f.request(t, host, http.MethodPut, "/api/v1/hosted/dns", token, map[string]any{"name": "app." + host, "type": "A", "values": []string{"192.0.2.99"}, "ttl": 300})
	if w.Code != http.StatusOK {
		t.Fatalf("tenant's own DNS name was rejected: %d %s", w.Code, w.Body.String())
	}
}

package hosted

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/doout/dispatch/internal/certificates"
	"github.com/doout/dispatch/internal/tenancy"
)

func TestCatalogSolverChallengeOwnership(t *testing.T) {
	f := newHostedFixture(t)
	f.tenant(t, "alpha", "owner-a")
	b := f.tenant(t, "bravo", "owner-b")
	ctx := context.Background()
	solver := catalogSolver{f.server}
	challenge := certificates.Challenge{ID: "order-a", OwnerID: platformOwner, Generation: 1, Name: "_acme-challenge." + f.server.Config.RootDomain, Value: "proof-a"}
	for _, name := range []string{"_acme-challenge.bravo." + f.server.Config.RootDomain, "_acme-challenge.alpha." + f.server.Config.RootDomain, "_acme-challenge.foreign.test"} {
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
	for _, ch := range []certificates.Challenge{newer, parallel} {
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

func TestWildcardMigrationPreservesCustomRecords(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	ctx := t.Context()
	host := tenant.Slug + "." + f.server.Config.RootDomain
	records := []tenancy.ZoneRecord{
		{ID: "old-console", OwnerID: "console:" + tenant.ID, TenantID: tenant.ID, Name: host, Type: "A", Values: []string{"192.0.2.10"}, TTL: 300},
		{ID: "old-wildcard", OwnerID: "workloads:" + tenant.ID, TenantID: tenant.ID, Name: "*." + host, Type: "CNAME", Values: []string{"gateway.example.net"}, TTL: 300},
		{ID: "custom", OwnerID: "records:" + tenant.ID, TenantID: tenant.ID, Name: "custom." + host, Type: "A", Values: []string{"192.0.2.42"}, TTL: 300},
	}
	for _, record := range records {
		if _, err := f.catalog.SaveZoneRecord(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.catalog.SaveDomain(ctx, tenancy.Domain{ID: "workloads:" + tenant.ID, TenantID: tenant.ID, Hostname: "*." + host, Kind: "workloads", State: tenancy.StateActive}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.prepareTenantDNS(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	_, remaining, err := f.catalog.ZoneRecords(ctx)
	if err != nil || len(remaining) != 1 || remaining[0].ID != "custom" {
		t.Fatal(remaining, err)
	}
	domain, err := f.catalog.DomainByHostname(ctx, "*."+host)
	if err != nil || domain.State != tenancy.StateDisabled {
		t.Fatal(domain, err)
	}
	for _, id := range []string{"old-console", "old-wildcard"} {
		change, err := f.catalog.DNSChange(ctx, id)
		if err != nil || !change.Delete {
			t.Fatal(id, change, err)
		}
	}
}

func TestHostedDNSRootIsImmutable(t *testing.T) {
	f := newHostedFixture(t)
	ctx := t.Context()
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
		t.Fatal("unchanged configuration advanced generation")
	}
	if len(f.dns.records) != 0 {
		t.Fatal("private root binding was published")
	}
	f.server.Config.RootDomain = "other.example.test"
	if err := f.server.Prepare(ctx); err == nil {
		t.Fatal("catalog accepted another root domain")
	}
	_, records, err := f.catalog.ZoneRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if strings.HasSuffix(record.Name, "other.example.test") {
			t.Fatal("failed root change altered records")
		}
	}
}

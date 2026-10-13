package hosted

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/doout/dispatch/internal/certificates"
	"github.com/doout/dispatch/internal/dnsprovider"
	"github.com/doout/dispatch/internal/tenancy"
)

type testDNSProvider struct {
	mu         sync.Mutex
	records    map[string]dnsprovider.Record
	failName   string
	failDelete bool
	writes     int
}

func (*testDNSProvider) Target() string { return "test:zone" }

func newTestDNSProvider() *testDNSProvider {
	return &testDNSProvider{records: make(map[string]dnsprovider.Record)}
}

func (p *testDNSProvider) Ensure(_ context.Context, r dnsprovider.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Name == p.failName {
		return errors.New("provider unavailable")
	}
	p.records[r.ID] = r
	p.writes++
	return nil
}

func (p *testDNSProvider) Delete(_ context.Context, r dnsprovider.Record) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failDelete {
		return errors.New("provider unavailable")
	}
	delete(p.records, r.ID)
	p.writes++
	return nil
}

func (p *testDNSProvider) Nameservers(context.Context) ([]string, error) {
	return []string{"ns1.example.net", "ns2.example.net"}, nil
}

func TestTenantCreationDoesNotPublishDNS(t *testing.T) {
	f := newHostedFixture(t)
	ctx := t.Context()
	f.dns.failDelete = true
	f.dns.failName = "blocked." + f.server.Config.RootDomain
	for _, slug := range []string{"blocked", "ready"} {
		if _, err := f.catalog.CreateTenant(ctx, tenancy.CreateTenantInput{Slug: slug, Name: slug, CreatorID: "platform", InitialOwnerID: "owner-a"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.server.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	tenants, err := f.catalog.ListTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range tenants {
		if tenant.State != tenancy.StateActive {
			t.Fatal(tenant)
		}
		if _, err := f.catalog.DomainByHostname(ctx, tenant.Slug+"."+f.server.Config.RootDomain); err != nil {
			t.Fatal(err)
		}
	}
	if f.dns.writes != 0 {
		t.Fatal("tenant creation called the DNS provider", f.dns.writes)
	}
	restarted, err := New(ctx, f.server.Config, f.catalog, f.server.OpenRuntime, f.server.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if f.dns.writes != 0 {
		t.Fatal("restart published tenant DNS")
	}
}

func TestTenantDNSAPIIsUnavailable(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	host := tenant.Slug + "." + f.server.Config.RootDomain
	token := f.session(t, "owner-a", tenancy.TenantAudience(tenant.ID))
	input := map[string]any{"name": "preview." + host, "type": "A", "values": []string{"192.0.2.22"}, "ttl": 300}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := f.request(t, host, method, "/api/v1/hosted/dns", token, input)
		if w.Code != 404 {
			t.Fatal(method, w.Code, w.Body.String())
		}
	}
	if f.dns.writes != 0 {
		t.Fatal("disabled API changed DNS")
	}
}

func TestLegacyTenantDNSDeletionRetriesAfterRestart(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	ctx := t.Context()
	record, err := f.catalog.SaveZoneRecord(ctx, tenancy.ZoneRecord{ID: "old-console", OwnerID: "console:" + tenant.ID, TenantID: tenant.ID, Name: "alpha." + f.server.Config.RootDomain, Type: "A", Values: []string{"192.0.2.10"}, TTL: 300})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.server.syncDNSRecord(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	f.dns.failDelete = true
	if err := f.server.ReconcileTenants(ctx); err == nil {
		t.Fatal("cleanup failure was ignored")
	}
	current, err := f.catalog.Tenant(ctx, tenant.ID)
	if err != nil || current.State != tenancy.StateActive {
		t.Fatal(current, err)
	}
	change, err := f.catalog.DNSChange(ctx, record.ID)
	if err != nil || !change.Delete {
		t.Fatal(change, err)
	}
	f.dns.failDelete = false
	restarted, err := New(ctx, f.server.Config, f.catalog, f.server.OpenRuntime, f.server.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.dns.records) != 0 {
		t.Fatal("legacy record remains", f.dns.records)
	}
	if _, err := f.catalog.DNSChange(ctx, record.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestCertificateDNSRetriesFailedPresentAndCleanup(t *testing.T) {
	f := newHostedFixture(t)
	ctx := t.Context()
	solver := catalogSolver{f.server}
	challenge := certificates.Challenge{ID: "order", OwnerID: platformOwner, Generation: 1, Name: "_acme-challenge." + f.server.Config.RootDomain, Value: "proof"}
	f.dns.failName = challenge.Name
	if err := solver.Present(ctx, challenge); err == nil {
		t.Fatal("failed present reported success")
	}
	change, err := f.catalog.DNSChange(ctx, challengeID(challenge))
	if err != nil || change.Delete {
		t.Fatal("challenge publication not queued", change, err)
	}
	f.dns.failName = ""
	if err := solver.Present(ctx, challenge); err != nil {
		t.Fatal("replay did not publish saved challenge", err)
	}
	if _, ok := f.dns.records[challengeID(challenge)]; !ok {
		t.Fatal("challenge absent from provider")
	}
	parallel := challenge
	parallel.Generation, parallel.Value = 2, "next-proof"
	if err := solver.Present(ctx, parallel); err != nil {
		t.Fatal(err)
	}
	f.dns.failDelete = true
	if err := solver.Cleanup(ctx, challenge); err == nil {
		t.Fatal("failed cleanup reported success")
	}
	f.dns.failDelete = false
	if err := solver.Cleanup(ctx, challenge); err != nil {
		t.Fatal("replay did not delete saved challenge", err)
	}
	if _, ok := f.dns.records[challengeID(challenge)]; ok {
		t.Fatal("old challenge remains")
	}
	if _, ok := f.dns.records[challengeID(parallel)]; !ok {
		t.Fatal("cleanup removed newer proof")
	}
}

func TestHostedStartupDoesNotWaitForDNSProvider(t *testing.T) {
	f := newHostedFixture(t)
	ctx := t.Context()
	legacy := tenancy.ZoneRecord{ID: "legacy-root", OwnerID: platformOwner, TenantID: platformOwner, Name: f.server.Config.RootDomain, Type: "A", Values: []string{"192.0.2.10"}, TTL: 300}
	if _, err := f.catalog.SaveZoneRecord(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	f.dns.failDelete = true
	if err := f.server.Prepare(ctx); err != nil {
		t.Fatal("provider outage prevented startup", err)
	}
	pending, err := f.catalog.DNSChange(ctx, legacy.ID)
	if err != nil || !pending.Delete {
		t.Fatal("legacy cleanup was lost", pending, err)
	}
	if err := f.server.ReconcileTenants(ctx); err == nil {
		t.Fatal("failed cleanup was ignored")
	}
	f.dns.failDelete = false
	if err := f.server.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.DNSChange(ctx, legacy.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("legacy cleanup not acknowledged", err)
	}
}

type otherDNSProvider struct{ *testDNSProvider }

func (otherDNSProvider) Target() string { return "test:other-zone" }

func TestHostedDNSProviderTargetIsBoundToCatalog(t *testing.T) {
	f := newHostedFixture(t)
	ctx := t.Context()
	if err := f.server.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	f.server.Config.DNSProvider = otherDNSProvider{f.dns}
	if err := f.server.Prepare(ctx); err == nil || !strings.Contains(err.Error(), "changing provider or zone requires a migration") {
		t.Fatal("accepted another publication target", err)
	}
	f.server.Config.DNSProvider = f.dns
	if err := f.server.Prepare(ctx); err != nil {
		t.Fatal("rejected original provider", err)
	}
}

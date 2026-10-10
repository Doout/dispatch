package hosted

import (
	"context"
	"encoding/json"
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

func TestTenantDNSPublicationRetriesAfterRestart(t *testing.T) {
	f := newHostedFixture(t)
	ctx := t.Context()
	tenants := make([]tenancy.Tenant, 0, 2)
	for _, slug := range []string{"blocked", "ready"} {
		tenant, err := f.catalog.CreateTenant(ctx, tenancy.CreateTenantInput{Slug: slug, Name: slug, CreatorID: "platform", InitialOwnerID: "owner-a"})
		if err != nil {
			t.Fatal(err)
		}
		tenants = append(tenants, tenant)
	}
	f.dns.failName = "blocked." + f.server.Config.RootDomain
	if err := f.server.ReconcileTenants(ctx); err == nil {
		t.Fatal("failed DNS publication was ignored")
	}
	for i, want := range []string{"failed", tenancy.StateActive} {
		tenant, err := f.catalog.Tenant(ctx, tenants[i].ID)
		if err != nil || tenant.State != want {
			t.Fatal(tenant, err)
		}
	}
	job, err := f.catalog.ProvisioningJob(ctx, tenants[0].ID)
	if err != nil || job.Phase != "configuring DNS" {
		t.Fatal(job, err)
	}
	if _, err := f.catalog.DomainByHostname(ctx, f.dns.failName); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("unpublished console became ready", err)
	}
	pending, err := f.catalog.PendingTenantDNSChanges(ctx, tenants[0].ID, 100)
	if err != nil || len(pending) != 1 {
		t.Fatal("failed record not queued", pending, err)
	}
	f.dns.failName = ""
	restarted, err := New(ctx, f.server.Config, f.catalog, f.server.OpenRuntime, f.server.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if err := restarted.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tenant := range tenants {
		current, err := f.catalog.Tenant(ctx, tenant.ID)
		if err != nil || current.State != tenancy.StateActive {
			t.Fatal(current, err)
		}
	}
	if len(f.dns.records) != 4 {
		t.Fatal("restart did not publish both tenants", f.dns.records)
	}
	writes := f.dns.writes
	if err := restarted.ReconcileTenants(ctx); err != nil {
		t.Fatal(err)
	}
	if writes != f.dns.writes {
		t.Fatal("unchanged records were republished")
	}
}

func TestTenantDNSAPIRetriesWritesAndDeletes(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	host := tenant.Slug + "." + f.server.Config.RootDomain
	token := f.session(t, "owner-a", tenancy.TenantAudience(tenant.ID))
	input := map[string]any{"name": "preview." + host, "type": "A", "values": []string{"192.0.2.22"}, "ttl": 300}
	f.dns.failName = "preview." + host
	w := f.request(t, host, http.MethodPut, "/api/v1/hosted/dns", token, input)
	var record dnsRecordResponse
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &record) != nil || record.SyncState != "pending" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = f.request(t, host, http.MethodGet, "/api/v1/hosted/dns", token, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"syncState":"pending"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	f.dns.failName = ""
	if err := f.server.ReconcileTenants(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.dns.records[record.ID]; !ok {
		t.Fatal("record was not published")
	}
	w = f.request(t, host, http.MethodGet, "/api/v1/hosted/dns", token, nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"syncState":"pending"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	input["generation"] = record.Generation
	f.dns.failDelete = true
	w = f.request(t, host, http.MethodDelete, "/api/v1/hosted/dns", token, input)
	if w.Code != http.StatusAccepted {
		t.Fatal(w.Code, w.Body.String())
	}
	change, err := f.catalog.DNSChange(t.Context(), record.ID)
	if err != nil || !change.Delete {
		t.Fatal("failed deletion was not saved", change, err)
	}
	restarted, err := New(t.Context(), f.server.Config, f.catalog, f.server.OpenRuntime, f.server.Logger)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	f.dns.failDelete = false
	if err := restarted.ReconcileTenants(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.dns.records[record.ID]; ok {
		t.Fatal("deleted record remains at provider")
	}
	if _, err := f.catalog.DNSChange(t.Context(), record.ID); !errors.Is(err, tenancy.ErrNotFound) {
		t.Fatal("delete not acknowledged", err)
	}
}

func TestGatewayPublicationWaitsForConflictingDelete(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	ctx := t.Context()
	f.server.Config.WorkloadGateway = "gateway.example.net"
	if err := f.server.prepareTenantDNS(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	f.server.Config.WorkloadGateway = "192.0.2.11"
	f.dns.failDelete = true
	if err := f.server.prepareTenantDNS(ctx, tenant); err == nil {
		t.Fatal("failed CNAME removal was ignored")
	}
	wildcard := "*." + tenant.Slug + "." + f.server.Config.RootDomain
	for _, record := range f.dns.records {
		if record.Name == wildcard && record.Type != "CNAME" {
			t.Fatal("conflicting record was published", record)
		}
	}
	f.dns.failDelete = false
	if err := f.server.prepareTenantDNS(ctx, tenant); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range f.dns.records {
		if record.Name == wildcard {
			if record.Type != "A" {
				t.Fatal("old CNAME remains", record)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("replacement A record not published")
	}
}

func TestCertificateDNSRetriesFailedPresentAndCleanup(t *testing.T) {
	f := newHostedFixture(t)
	tenant := f.tenant(t, "alpha", "owner-a")
	ctx := t.Context()
	solver := catalogSolver{f.server}
	challenge := certificates.Challenge{ID: "order", OwnerID: tenant.ID, Generation: 1, Name: "_acme-challenge.alpha." + f.server.Config.RootDomain, Value: "proof"}
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

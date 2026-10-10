package tenancy

import (
	"context"
	"errors"
	"net/url"
	"os"
	"reflect"
	"testing"
)

func eachDNSCatalog(t *testing.T, check func(*testing.T, *Catalog)) {
	t.Helper()
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) { check(t, testCatalog(t, postgres)) })
	}
}
func dnsTestRecord(id string) ZoneRecord {
	return ZoneRecord{ID: id, OwnerID: "records:alpha", TenantID: "alpha", Name: id + ".alpha.example.test", Type: "TXT", Values: []string{"value"}, TTL: 60}
}
func pendingDNS(t *testing.T, c *Catalog) []DNSChange {
	t.Helper()
	changes, err := c.PendingDNSChanges(t.Context(), 1000)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}
func saveDNS(t *testing.T, c *Catalog, r ZoneRecord) ZoneRecord {
	t.Helper()
	r, err := c.SaveZoneRecord(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func dnsCatalogURL(t *testing.T, c *Catalog) string {
	t.Helper()
	ctx := t.Context()
	var dsn string
	if c.postgres {
		var searchPath string
		if err := c.db.QueryRowContext(ctx, `SELECT current_setting('search_path')`).Scan(&searchPath); err != nil {
			t.Fatal(err)
		}
		dsn = os.Getenv("DISPATCH_TEST_POSTGRES_URL")
		if dsn == "" {
			dsn = os.Getenv("DISPATCH_STORE_POSTGRES_URL")
		}
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := parsed.Query()
		q.Set("search_path", searchPath)
		parsed.RawQuery = q.Encode()
		dsn = parsed.String()
	} else {
		var sequence int
		var name string
		if err := c.db.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &name, &dsn); err != nil {
			t.Fatal(err)
		}
	}
	return dsn
}

func reopenDNSCatalog(t *testing.T, c *Catalog) *Catalog {
	t.Helper()
	ctx := t.Context()
	dsn := dnsCatalogURL(t, c)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if err := reopened.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return reopened
}

func TestDNSChangesCoalesceWithoutLosingNewerGenerations(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		first := saveDNS(t, c, dnsTestRecord("first"))
		second := saveDNS(t, c, dnsTestRecord("second"))
		updated := first
		updated.Values = []string{"updated"}
		updated = saveDNS(t, c, updated)
		changes := pendingDNS(t, c)
		expected := []DNSChange{{Record: second, Generation: second.Generation}, {Record: updated, Generation: updated.Generation}}
		if !reflect.DeepEqual(changes, expected) {
			t.Fatalf("coalesced changes=%+v want %+v", changes, expected)
		}
		limited, err := c.PendingDNSChanges(ctx, 1)
		if err != nil || !reflect.DeepEqual(limited, expected[:1]) {
			t.Fatal(limited, err)
		}
		if err = c.CompleteDNSChange(ctx, first.ID, first.Generation); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pendingDNS(t, c), expected) {
			t.Fatal("stale acknowledgment removed newer change")
		}
		if err = c.CompleteDNSChange(ctx, updated.ID, updated.Generation); err != nil {
			t.Fatal(err)
		}
		if err = c.CompleteDNSChange(ctx, updated.ID, updated.Generation); err != nil {
			t.Fatal("acknowledgment not idempotent", err)
		}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, expected[:1]) {
			t.Fatal(got)
		}
		if err := c.DeleteZoneRecord(ctx, second.ID, second.OwnerID, second.Generation); err != nil {
			t.Fatal(err)
		}
		deletion := []DNSChange{{Record: second, Generation: updated.Generation + 1, Delete: true}}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, deletion) {
			t.Fatal("delete used record version instead of global generation", got)
		}
		if err := c.CompleteDNSChange(ctx, second.ID, second.Generation); err != nil {
			t.Fatal(err)
		}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, deletion) {
			t.Fatal("old save completion removed pending deletion", got)
		}
	})
}

func TestDNSDeletionSurvivesRestartAndRecreation(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		record := saveDNS(t, c, dnsTestRecord("record"))
		if err := c.CompleteDNSChange(ctx, record.ID, record.Generation); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteZoneRecord(ctx, record.ID, record.OwnerID, record.Generation); err != nil {
			t.Fatal(err)
		}
		expected := []DNSChange{{Record: record, Generation: record.Generation + 1, Delete: true}}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, expected) {
			t.Fatal(got)
		}
		// A failed provider attempt does not acknowledge the change. Reading and
		// restarting must leave the original deletion body available for retry.
		c = reopenDNSCatalog(t, c)
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, expected) {
			t.Fatal("deletion changed after restart", got)
		}
		generation, records, err := c.ZoneRecords(ctx)
		if err != nil || generation != expected[0].Generation || len(records) != 0 {
			t.Fatal(generation, records, err)
		}
		recreated := dnsTestRecord(record.ID)
		recreated.Values = []string{"replacement"}
		recreated = saveDNS(t, c, recreated)
		if recreated.Generation <= expected[0].Generation {
			t.Fatal("recreation did not advance version")
		}
		if err = c.CompleteDNSChange(ctx, record.ID, expected[0].Generation); err != nil {
			t.Fatal(err)
		}
		want := []DNSChange{{Record: recreated, Generation: recreated.Generation}}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, want) {
			t.Fatal("old delete cleared recreated record", got)
		}
	})
}

func TestDNSChangesRollbackWithRecordAndGlobalGeneration(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		record := saveDNS(t, c, dnsTestRecord("record"))
		before := pendingDNS(t, c)
		if c.postgres {
			if _, err := c.db.ExecContext(ctx, `CREATE FUNCTION reject_dns_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test publication failure'; END $$; CREATE TRIGGER reject_dns_changes BEFORE INSERT OR UPDATE ON tenant_dns_changes FOR EACH ROW EXECUTE FUNCTION reject_dns_change()`); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := c.db.ExecContext(ctx, `CREATE TRIGGER reject_dns_changes BEFORE INSERT ON tenant_dns_changes BEGIN SELECT RAISE(ABORT,'test publication failure'); END`); err != nil {
				t.Fatal(err)
			}
		}
		changed := record
		changed.Values = []string{"must-not-save"}
		for _, r := range []ZoneRecord{changed, dnsTestRecord("new")} {
			if _, err := c.SaveZoneRecord(ctx, r); err == nil {
				t.Fatal("save succeeded without its publication")
			}
		}
		if err := c.DeleteZoneRecord(ctx, record.ID, record.OwnerID, record.Generation); err == nil {
			t.Fatal("delete succeeded without its publication")
		}
		generation, records, err := c.ZoneRecords(ctx)
		if err != nil || generation != record.Generation || !reflect.DeepEqual(records, []ZoneRecord{record}) {
			t.Fatal("record transaction partially committed", generation, records, err)
		}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, before) {
			t.Fatal("failed write changed publication", got)
		}
	})
}

func TestDNSMigrationPublishesExistingRecordsOnlyOnce(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		existing := saveDNS(t, c, dnsTestRecord("existing"))
		marker := dnsTestRecord("platform-zone-config")
		saveDNS(t, c, marker)
		if _, err := c.db.ExecContext(ctx, `DELETE FROM tenant_dns_changes`); err != nil {
			t.Fatal(err)
		}
		// Recreate the prior catalog state: DNS records exist, migration 2 has not run.
		if _, err := c.db.ExecContext(ctx, `DELETE FROM tenancy_schema WHERE version=2`); err != nil {
			t.Fatal(err)
		}
		if err := c.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		expected := []DNSChange{{Record: existing, Generation: existing.Generation}}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, expected) {
			t.Fatal("existing record was not bootstrapped", got)
		}
		if err := c.CompleteDNSChange(ctx, existing.ID, existing.Generation); err != nil {
			t.Fatal(err)
		}
		c = reopenDNSCatalog(t, c)
		if got := pendingDNS(t, c); len(got) != 0 {
			t.Fatal("restart republished completed records", got)
		}
		if err := c.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if got := pendingDNS(t, c); len(got) != 0 {
			t.Fatal("repeat migration republished completed records", got)
		}
	})
}

func TestDNSScopedPublicationAndMarkerExclusion(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		alpha := saveDNS(t, c, dnsTestRecord("alpha-record"))
		bravo := dnsTestRecord("bravo-record")
		bravo.TenantID = "bravo"
		bravo = saveDNS(t, c, bravo)
		marker := saveDNS(t, c, dnsTestRecord("platform-zone-config"))
		marker.Values = []string{"changed"}
		marker = saveDNS(t, c, marker)
		if err := c.DeleteZoneRecord(ctx, marker.ID, marker.OwnerID, marker.Generation); err != nil {
			t.Fatal(err)
		}
		scoped, err := c.PendingTenantDNSChanges(ctx, "bravo", 1)
		if err != nil || !reflect.DeepEqual(scoped, []DNSChange{{Record: bravo, Generation: bravo.Generation}}) {
			t.Fatal(scoped, err)
		}
		change, err := c.DNSChange(ctx, alpha.ID)
		if err != nil || !reflect.DeepEqual(change, DNSChange{Record: alpha, Generation: alpha.Generation}) {
			t.Fatal(change, err)
		}
		if _, err := c.DNSChange(ctx, marker.ID); !errors.Is(err, ErrNotFound) {
			t.Fatal("internal zone marker was queued", err)
		}
		if _, err := c.DNSChange(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if got := pendingDNS(t, c); len(got) != 2 {
			t.Fatal(got)
		}
		for _, limit := range []int{-1, 0, 1001} {
			if _, err := c.PendingDNSChanges(ctx, limit); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid limit accepted", limit, err)
			}
		}
		if _, err := c.PendingTenantDNSChanges(ctx, "", 1); !errors.Is(err, ErrInvalid) {
			t.Fatal("empty tenant scope accepted", err)
		}
	})
}

// Keep cancellation from acknowledging an unpublished change.
func TestDNSCompletionHonorsCancelledContext(t *testing.T) {
	c := testCatalog(t, false)
	record := saveDNS(t, c, dnsTestRecord("record"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.CompleteDNSChange(ctx, record.ID, record.Generation); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(pendingDNS(t, c)) != 1 {
		t.Fatal("cancelled completion cleared pending work")
	}
}

func TestDNSRecreationPreservesIdentityUntilDeletionIsPublished(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		record := saveDNS(t, c, dnsTestRecord("record"))
		if err := c.DeleteZoneRecord(ctx, record.ID, record.OwnerID, record.Generation); err != nil {
			t.Fatal(err)
		}
		before := pendingDNS(t, c)
		for _, field := range []string{"owner", "tenant", "name", "type"} {
			changed := record
			changed.Generation = 0
			switch field {
			case "owner":
				changed.OwnerID = "other"
			case "tenant":
				changed.TenantID = "bravo"
			case "name":
				changed.Name = "other.alpha.example.test"
			case "type":
				changed.Type = "A"
			}
			if _, err := c.SaveZoneRecord(ctx, changed); !errors.Is(err, ErrDenied) {
				t.Fatalf("recreated record changed %s: %v", field, err)
			}
		}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, before) {
			t.Fatal("rejected recreation lost tombstone", got)
		}
		generation, records, err := c.ZoneRecords(ctx)
		if err != nil || generation != before[0].Generation || len(records) != 0 {
			t.Fatal("rejected recreation changed DNS", generation, records, err)
		}
		// Once the provider has removed the old identity, the old ID is no longer
		// protecting an unpublished external record and normal new-record rules apply.
		if err := c.CompleteDNSChange(ctx, record.ID, before[0].Generation); err != nil {
			t.Fatal(err)
		}
		replacement := record
		replacement.Generation = 0
		replacement.Name = "replacement.alpha.example.test"
		saveDNS(t, c, replacement)
	})
}

func TestDNSMigrationRetriesAfterFailedPublicationBootstrap(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		record := saveDNS(t, c, dnsTestRecord("record"))
		if _, err := c.db.ExecContext(ctx, `DELETE FROM tenant_dns_changes`); err != nil {
			t.Fatal(err)
		}
		if _, err := c.db.ExecContext(ctx, `DELETE FROM tenancy_schema WHERE version=2`); err != nil {
			t.Fatal(err)
		}
		if c.postgres {
			if _, err := c.db.ExecContext(ctx, `CREATE FUNCTION reject_dns_bootstrap() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test publication failure'; END $$; CREATE TRIGGER reject_dns_bootstrap BEFORE INSERT ON tenant_dns_changes FOR EACH ROW EXECUTE FUNCTION reject_dns_bootstrap()`); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err := c.db.ExecContext(ctx, `CREATE TRIGGER reject_dns_bootstrap BEFORE INSERT ON tenant_dns_changes BEGIN SELECT RAISE(ABORT,'test publication failure'); END`); err != nil {
				t.Fatal(err)
			}
		}
		if err := c.Migrate(ctx); err == nil {
			t.Fatal("migration succeeded with missing publication")
		}
		var markers int
		if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tenancy_schema WHERE version=2`).Scan(&markers); err != nil || markers != 0 {
			t.Fatal("failed migration marked complete", markers, err)
		}
		drop := `DROP TRIGGER reject_dns_bootstrap`
		if c.postgres {
			drop += ` ON tenant_dns_changes`
		}
		if _, err := c.db.ExecContext(ctx, drop); err != nil {
			t.Fatal(err)
		}
		if err := c.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if got := pendingDNS(t, c); !reflect.DeepEqual(got, []DNSChange{{Record: record, Generation: record.Generation}}) {
			t.Fatal("migration retry lost publication", got)
		}
	})
}

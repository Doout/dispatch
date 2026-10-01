package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestInfrastructureQuotaSQLite(t *testing.T) {
	testInfrastructureQuota(t, filepath.Join(t.TempDir(), "quota.db"))
}
func TestInfrastructureQuotaPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_QUOTA_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_QUOTA_POSTGRES_URL to a disposable database")
	}
	testInfrastructureQuota(t, dsn)
}
func testInfrastructureQuota(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = s.CreateProject(ctx, core.Project{ID: "quota-project", Name: "Quota", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	change := core.InfrastructureQuotaChange{ProjectID: "quota-project", ProviderID: "quota-provider", OperationID: "quota-operation", ServerID: "quota-server", Region: "eu-1", Size: "small", Action: "server.create"}
	apply := func(c core.InfrastructureQuotaChange, commit bool) error {
		tx, e := s.db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		defer tx.Rollback()
		if e = s.ApplyInfrastructureQuota(ctx, tx.Tx, c, now); e != nil {
			return e
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	requireViolation := func(err error, limit string) {
		t.Helper()
		var violation *core.InfrastructureQuotaViolation
		if !errors.As(err, &violation) || violation.Limit != limit {
			t.Fatalf("wanted %s violation; got %v", limit, err)
		}
	}
	requireViolation(apply(change, true), "maxServers")
	policy := core.InfrastructureQuotaPolicy{ProjectID: change.ProjectID, Revision: 1, MaxServers: 3, Providers: []core.InfrastructureProviderRule{{ProviderID: change.ProviderID, Regions: []string{change.Region}, Sizes: []string{change.Size}}}, UpdatedAt: now}
	if err = s.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"provider", "region", "size"} {
		c := change
		switch kind {
		case "provider":
			c.ProviderID = "disallowed"
		case "region":
			c.Region = "disallowed"
		case "size":
			c.Size = "disallowed"
		}
		requireViolation(apply(c, true), kind)
	}
	if err = apply(change, false); err != nil {
		t.Fatal(err)
	}
	reservations, err := s.ListInfrastructureQuotaReservations(ctx, change.ProjectID)
	if err != nil || len(reservations) != 0 {
		t.Fatal("rolled back intent consumed quota", reservations, err)
	}
	if err = apply(change, true); err != nil {
		t.Fatal(err)
	}
	if err = apply(change, true); err != nil {
		t.Fatal("replay reserved twice", err)
	}
	changed := change
	changed.ProjectID = "different"
	if err = apply(changed, true); !errors.Is(err, ErrQuotaReservationChanged) {
		t.Fatal("ownership changed", err)
	}
	// Independent acceptance transactions share one project limit.
	results := make(chan error, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := change
			c.ServerID = fmt.Sprintf("concurrent-%d", i)
			c.OperationID = "op-" + c.ServerID
			results <- apply(c, true)
		}(i)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for e := range results {
		if e == nil {
			accepted++
		} else {
			requireViolation(e, "maxServers")
		}
	}
	if accepted != 2 {
		t.Fatalf("quota admitted %d plus initial reservation, want 3 total", accepted)
	}
	reservations, err = s.ListInfrastructureQuotaReservations(ctx, change.ProjectID)
	if err != nil || len(reservations) != 3 {
		t.Fatal(reservations, err)
	}
	// Allocation stays charged during failed bootstrap and uncertain deletion.
	c := change
	c.Action = "server.created"
	c.ResourceID = "provider-resource"
	if err = apply(c, true); err != nil {
		t.Fatal(err)
	}
	c.Action = "server.delete"
	c.OperationID = "delete-op"
	if err = apply(c, true); err != nil {
		t.Fatal(err)
	}
	c.Action = "server.unresolved"
	if err = apply(c, true); err != nil {
		t.Fatal(err)
	}
	c = change
	c.ServerID = "after-unknown"
	c.OperationID = "op-after-unknown"
	requireViolation(apply(c, true), "maxServers")
	// Policy reductions preserve existing resources and prevent new allocation.
	policy.Revision = 2
	policy.MaxServers = 1
	if err = s.SaveInfrastructureQuotaPolicy(ctx, policy, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveInfrastructureQuotaPolicy(ctx, policy, 1); !errors.Is(err, ErrQuotaPolicyChanged) {
		t.Fatal("stale policy update accepted", err)
	}
	requireViolation(apply(c, true), "maxServers")
	c = change
	c.Action = "server.deleted"
	c.OperationID = "delete-op"
	c.ResourceID = "provider-resource"
	if err = apply(c, true); err != nil {
		t.Fatal(err)
	}
	if err = apply(change, true); !errors.Is(err, ErrQuotaReservationChanged) {
		t.Fatal("retired create identity reused", err)
	}
	// Cancellation releases only an accepted create that never allocated a resource.
	for _, r := range reservations {
		if r.ServerID == change.ServerID {
			continue
		}
		c = core.InfrastructureQuotaChange{ProjectID: r.ProjectID, ProviderID: r.ProviderID, OperationID: r.OperationID, ServerID: r.ServerID, Action: "server.cancelled"}
		if err = apply(c, true); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening the store preserves reservations and configured policy.
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	current, err := s.GetInfrastructureQuotaPolicy(ctx, change.ProjectID)
	if err != nil || current.MaxServers != 1 || current.Revision != 2 {
		t.Fatal(current, err)
	}
	c = change
	c.ServerID = "after-restart"
	c.OperationID = "op-after-restart"
	if err = apply(c, true); err != nil {
		t.Fatal("confirmed cleanup did not release capacity", err)
	}
	reservations, err = s.ListInfrastructureQuotaReservations(ctx, change.ProjectID)
	if err != nil || len(reservations) != 4 {
		t.Fatal("recovery evidence lost", reservations, err)
	}
}

func TestInfrastructureQuotaMigrationCountsExistingAllocation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = s.CreateProject(ctx, core.Project{ID: "existing-project", Name: "Existing", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := core.InfrastructureProvider{ID: "existing-provider", Name: "Provider", Endpoint: "https://provider.example", Enabled: true, Capabilities: []string{"server.create", "server.inspect"}, Manifest: json.RawMessage(`{}`), ManifestDigest: "manifest", State: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateInfrastructureProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	review := core.InfrastructureReview{ID: "existing-review", ServerID: "existing-server", ProjectID: "existing-project", ProviderID: p.ID, ProviderRevision: p.Revision, ManifestDigest: p.ManifestDigest, Name: "Existing server", Input: json.RawMessage(`{"region":"eu-1","size":"small"}`), EncryptedRequest: "encrypted", Digest: "review-digest", State: "open", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err = s.CreateInfrastructureReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	server, op, err := s.AcceptInfrastructureReview(ctx, review.ID, review.Digest, "existing-create", "owner", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	server.AllocationState = "allocated"
	server.ResourceID = "provider-machine"
	server.Revision++
	if err = s.UpdateManagedServer(ctx, server, server.Revision-1); err != nil {
		t.Fatal(err)
	}
	// Reapply the quota migration to a database that already has owned machines.
	for _, query := range []string{`DROP TABLE infrastructure_quota_reservations`, `DROP TABLE project_infrastructure_policies`, `DELETE FROM schema_migrations WHERE version='079_infrastructure_quotas'`} {
		if _, err = s.db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListInfrastructureQuotaReservations(ctx, server.ProjectID)
	if err != nil || len(items) != 1 || items[0].OperationID != op.ID || items[0].State != "allocated" || items[0].ResourceID != server.ResourceID || items[0].Region != "eu-1" || items[0].Size != "small" {
		t.Fatal("existing resource disappeared from accounting", items, err)
	}
	policy := core.InfrastructureQuotaPolicy{ProjectID: server.ProjectID, Revision: 1, MaxServers: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, AnyRegion: true, AnySize: true}}, UpdatedAt: now}
	if err = s.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = s.ApplyInfrastructureQuota(ctx, tx.Tx, core.InfrastructureQuotaChange{ProjectID: server.ProjectID, ProviderID: p.ID, ServerID: "new-server", OperationID: "new-create", Region: "eu-1", Size: "small", Action: "server.create"}, now)
	var violation *core.InfrastructureQuotaViolation
	if !errors.As(err, &violation) || violation.Usage != 1 {
		t.Fatal("upgrade allowed quota bypass", err)
	}
}

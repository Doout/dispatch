package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestSnapshotAdmissionSQLite(t *testing.T) {
	testSnapshotAdmission(t, filepath.Join(t.TempDir(), "snapshots.db"))
}
func TestSnapshotAdmissionPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_PROVIDER_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_PROVIDER_POSTGRES_URL for disposable PostgreSQL")
	}
	admin, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "snapshots_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.db.ExecContext(context.Background(), `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer admin.db.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	testSnapshotAdmission(t, u.String())
}
func testSnapshotAdmission(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = s.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := core.InfrastructureProvider{ID: "provider", Name: "provider", Endpoint: "http://127.0.0.1:1234", Enabled: true, Capabilities: []string{"snapshot.create"}, Manifest: json.RawMessage(`{}`), ManifestDigest: "manifest", State: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateInfrastructureProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	reviews := []core.InfrastructureSnapshotReview{}
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("source-%d", i)
		r := core.InfrastructureReview{ID: "review-" + id, ServerID: id, ProjectID: "project", ProviderID: p.ID, ProviderRevision: 1, ManifestDigest: p.ManifestDigest, Name: id, Input: json.RawMessage(`{}`), EncryptedRequest: "cipher", Digest: "digest-" + id, State: "open", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
		if err = s.CreateInfrastructureReview(ctx, r); err != nil {
			t.Fatal(err)
		}
		m, op, err := s.AcceptInfrastructureReview(ctx, r.ID, r.Digest, "create-"+id, "actor", now, nil)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := s.LeaseInfrastructureOperation(ctx, now, time.Minute)
		if err != nil || lease == nil || lease.ID != op.ID {
			t.Fatal(lease, err)
		}
		m.ResourceID = "resource-" + id
		m.AllocationState = "allocated"
		m.Revision++
		lease.State = "succeeded"
		if err = s.CompleteInfrastructureOperation(ctx, m, *lease, now, nil); err != nil {
			t.Fatal(err)
		}
		sr := core.InfrastructureSnapshotReview{ID: "snapshot-review-" + id, SnapshotID: "snapshot-" + id, ServerID: id, ProjectID: "project", ProviderID: p.ID, ProviderRevision: 1, ManifestDigest: p.ManifestDigest, Name: "capture-" + id, Action: "snapshot.create", Input: json.RawMessage(`{}`), EncryptedRequest: "cipher", Digest: "digest-" + id, State: "open", RetainUntil: now, ExpiresAt: now.Add(time.Hour), CreatedAt: now}
		if err = s.CreateInfrastructureSnapshotReview(ctx, sr); err != nil {
			t.Fatal(err)
		}
		reviews = append(reviews, sr)
	}
	if _, _, err = s.AcceptInfrastructureSnapshotReview(ctx, reviews[0].ID, reviews[0].Digest, "capture-0", "actor", now, s.InfrastructureQuotaAdmission); err == nil {
		t.Fatal("missing snapshot policy admitted allocation")
	}
	policy := core.InfrastructureQuotaPolicy{ProjectID: "project", Revision: 1, MaxSnapshots: 1, Providers: []core.InfrastructureProviderRule{{ProviderID: p.ID, AnyRegion: true, AnySize: true}}, UpdatedAt: now}
	if err = s.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	accepted := make(chan core.InfrastructureOperation, 6)
	failures := make(chan error, 6)
	for i, r := range reviews {
		wg.Add(1)
		go func(i int, r core.InfrastructureSnapshotReview) {
			defer wg.Done()
			_, op, err := s.AcceptInfrastructureSnapshotReview(ctx, r.ID, r.Digest, fmt.Sprintf("capture-%d", i), "actor", now, s.InfrastructureQuotaAdmission)
			if err == nil {
				accepted <- op
			} else {
				failures <- err
			}
		}(i, r)
	}
	wg.Wait()
	close(accepted)
	close(failures)
	if len(accepted) != 1 {
		t.Fatalf("quota admitted %d concurrent snapshots", len(accepted))
	}
	op := <-accepted
	for err := range failures {
		var violation *core.InfrastructureQuotaViolation
		if !errors.As(err, &violation) || violation.Limit != "maxSnapshots" {
			t.Fatal("wrong quota rejection", err)
		}
	}
	review, _ := s.SnapshotReviewForOperation(ctx, op.ID)
	snapshot, _ := s.GetInfrastructureSnapshot(ctx, review.SnapshotID)
	lease, err := s.LeaseInfrastructureOperation(ctx, now, time.Minute)
	if err != nil || lease == nil || lease.ID != op.ID {
		t.Fatal(lease, err)
	}
	lease.State = "unknown"
	snapshot.State = "unknown"
	snapshot.Revision++
	snapshot.UpdatedAt = now
	if err = s.CompleteInfrastructureSnapshot(ctx, snapshot, *lease, now, s.InfrastructureQuotaAdmission); err != nil {
		t.Fatal(err)
	}
	other := reviews[0]
	if other.ID == review.ID {
		other = reviews[1]
	}
	if _, _, err = s.AcceptInfrastructureSnapshotReview(ctx, other.ID, other.Digest, "later-capture", "actor", now, s.InfrastructureQuotaAdmission); err == nil {
		t.Fatal("unknown snapshot released quota")
	}
	// Expired worker leases cannot release quota. Explicit resolved absence can.
	stale := snapshot
	stale.State = "deleted"
	stale.Revision++
	lease.State = "succeeded"
	if err = s.CompleteInfrastructureSnapshot(ctx, stale, *lease, now, s.InfrastructureQuotaAdmission); !errors.Is(err, ErrInfrastructureChanged) {
		t.Fatal("stale worker changed terminal result", err)
	}
	snapshot.State = "deleted"
	snapshot.ResourceID = "verified-absent"
	if err = s.ResolveInfrastructureSnapshot(ctx, snapshot, op.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInfrastructureSnapshotReview(ctx, other.ID, other.Digest, "later-capture", "actor", now, s.InfrastructureQuotaAdmission); err != nil {
		t.Fatal("verified absence did not release capacity", err)
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestInfrastructureLifecycleSQLite(t *testing.T) {
	testInfrastructureLifecycle(t, filepath.Join(t.TempDir(), "lifecycle.db"))
}
func TestInfrastructureLifecyclePostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_PROVIDER_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_PROVIDER_POSTGRES_URL to a disposable database")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "lifecycle_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer admin.db.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`)
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	testInfrastructureLifecycle(t, parsed.String())
}
func testInfrastructureLifecycle(t *testing.T, dsn string) {
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
	prefix := ulid.Make().String()
	now := time.Now().UTC()
	project := core.Project{ID: prefix + "p", Name: "Project", CreatedAt: now}
	if err = s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	p := core.InfrastructureProvider{ID: prefix + "provider", Name: "Mock", Endpoint: "http://127.0.0.1:1234", Enabled: true, Capabilities: []string{"server.create"}, Manifest: json.RawMessage(`{}`), ManifestDigest: "manifest", State: "ready", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateInfrastructureProvider(ctx, p); err != nil {
		t.Fatal(err)
	}
	r := core.InfrastructureReview{ID: prefix + "review", ServerID: prefix + "server", ProjectID: project.ID, ProviderID: p.ID, ProviderRevision: 1, ManifestDigest: p.ManifestDigest, Name: "Machine", Input: json.RawMessage(`{"region":"mock-region","size":"mock-small"}`), EncryptedRequest: "ciphertext", Digest: "digest", State: "open", ExpiresAt: now.Add(time.Minute), CreatedAt: now}
	if err = s.CreateInfrastructureReview(ctx, r); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("quota exceeded")
	admission := func(_ context.Context, _ *sql.Tx, accept core.InfrastructureAcceptance) error {
		if accept.ProjectID != project.ID || accept.ActorID != "actor" || string(accept.DesiredConfig) != string(r.Input) {
			t.Fatal("admission context incomplete", accept)
		}
		return rejected
	}
	if _, _, err = s.AcceptInfrastructureReview(ctx, r.ID, r.Digest, prefix+"operation", "actor", now, admission); !errors.Is(err, rejected) {
		t.Fatal("admission rejection ignored", err)
	}
	saved, _ := s.GetInfrastructureReview(ctx, r.ID)
	if saved.State != "open" {
		t.Fatal("rejected review consumed")
	}
	managed, op, err := s.AcceptInfrastructureReview(ctx, r.ID, r.Digest, prefix+"operation", "actor", now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, old, err := s.AcceptInfrastructureReview(ctx, r.ID, r.Digest, op.ID, "actor", now, nil); err != nil || old.ID != op.ID {
		t.Fatal("idempotent acceptance failed", err)
	}
	leased, err := s.LeaseInfrastructureOperation(ctx, now, time.Minute)
	if err != nil || leased == nil || leased.ID != op.ID {
		t.Fatal("lease", err)
	}
	if err = s.CancelInfrastructureOperation(ctx, op.ID, now); err != nil {
		t.Fatal(err)
	}
	leased.Stage = "submitting"
	leased.State = "running"
	leased.NextAttemptAt = now
	if err = s.CheckpointInfrastructureOperation(ctx, *leased, true, now); err != nil {
		t.Fatal(err)
	}
	savedOp, _ := s.GetInfrastructureOperation(ctx, op.ID)
	if !savedOp.CancelRequested {
		t.Fatal("checkpoint erased cancellation")
	}
	leased, err = s.LeaseInfrastructureOperation(ctx, now, time.Minute)
	if err != nil || leased == nil {
		t.Fatal("re-lease", err)
	}
	leased.State = "unknown"
	leased.ResourceID = "owned"
	if err = s.CheckpointInfrastructureOperation(ctx, *leased, true, now); err != nil {
		t.Fatal(err)
	}
	managed, err = s.AdoptInfrastructureServer(ctx, managed, "owned", "192.0.2.10", now)
	if err != nil {
		t.Fatal(err)
	}
	managed.RuntimeState = "ready"
	managed.EnrollmentState = "enrolled"
	managed.Revision++
	managed.UpdatedAt = now
	if err = s.UpdateManagedServer(ctx, managed, managed.Revision-1); err != nil {
		t.Fatal(err)
	}
	target := core.Server{ID: managed.ID, ProjectID: managed.ProjectID, Name: managed.Name, Address: managed.Address, Runtime: core.ServerRuntimeDocker, AgentNodeID: managed.NodeID, State: "ready", CreatedAt: now}
	if err = s.CreateServer(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateApp(ctx, core.App{ID: prefix + "app", ProjectID: project.ID, ServerID: target.ID, Name: "App", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	deletion := core.InfrastructureOperation{ID: prefix + "delete", ActorID: "actor", ServerID: target.ID, ProviderID: p.ID, Action: "delete", State: "pending", Stage: "submit", ResourceID: "owned", RequestDigest: "delete-digest", ExpiresAt: now.Add(time.Minute), NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateInfrastructureDeletion(ctx, managed, deletion, now, nil); !errors.Is(err, ErrInfrastructureProtected) {
		t.Fatal("application guard", err)
	}
	if err = s.DeleteApp(ctx, prefix+"app"); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateInfrastructureDeletion(ctx, managed, deletion, now, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateApp(ctx, core.App{ID: prefix + "late", ProjectID: project.ID, ServerID: target.ID, Name: "Late", CreatedAt: now}); err == nil {
		t.Fatal("late app bypassed deletion")
	}
	if err = s.CreateService(ctx, core.Service{ID: prefix + "service", ProjectID: project.ID, Name: "Service", Revision: 1, ProvisionTarget: &core.ServiceProvisionTarget{ServerID: target.ID}}); err == nil {
		t.Fatal("late service bypassed deletion")
	}
	target.AgentNodeID = "different-node"
	if err = s.UpdateServer(ctx, target); err == nil {
		t.Fatal("managed identity changed")
	}
	if err = s.DeleteServer(ctx, target.ID); err == nil {
		t.Fatal("managed resource orphaned")
	}
}

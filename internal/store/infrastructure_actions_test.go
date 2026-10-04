package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// Both infrastructure lifecycle dialect gates execute this helper. The store
// enforces admission atomicity; provider evidence belongs to controller tests.
func testInfrastructureMachineActions(t *testing.T, s *SQLStore, m core.ManagedServer, now time.Time) core.ManagedServer {
	t.Helper()
	ctx := context.Background()
	m.PowerState = "running"
	m.Revision++
	if err := s.UpdateManagedServer(ctx, m, m.Revision-1); err != nil {
		t.Fatal(err)
	}
	p, err := s.GetInfrastructureProvider(ctx, m.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	op := core.InfrastructureOperation{ID: m.ID + "-power", ActorID: "actor", ServerID: m.ID, ProviderID: m.ProviderID, Action: "server.stop", State: "pending", Stage: "submit", ResourceID: m.ResourceID, RequestDigest: "power-digest", ExpiresAt: now.Add(time.Minute), NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	payload := core.InfrastructureActionRequest{OperationID: op.ID, ProviderRevision: p.Revision, ManifestDigest: p.ManifestDigest, EncryptedRequest: "wrapped-action-fixture", CipherDigest: "fixture-cipher-digest"}
	desired := m
	desired.PowerState = "transitioning"
	rejected := errors.New("action admission rejected")
	reject := func(context.Context, *sql.Tx, core.InfrastructureAcceptance) error { return rejected }
	if _, _, err = s.AcceptInfrastructureAction(ctx, desired, op, payload, now, reject); !errors.Is(err, rejected) {
		t.Fatal("action admission rejection ignored", err)
	}
	current, _ := s.GetManagedServer(ctx, m.ID)
	if current.Revision != m.Revision || current.PowerState != "running" || current.RuntimeState != "ready" {
		t.Fatal("rejected action changed target", current)
	}
	if _, err = s.GetInfrastructureOperation(ctx, op.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected action saved operation", err)
	}
	if _, err = s.GetInfrastructureActionRequest(ctx, op.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected action saved ciphertext", err)
	}
	// An existing application remains owned across power changes. Its admitted
	// runtime work blocks power acceptance, then the accepted action blocks new work.
	if err = s.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: m.NodeID, Name: "Action node", Driver: "agent", Config: map[string]string{}, Details: map[string]string{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = s.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: m.NodeID, EnrollmentHash: "action-enrollment", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = s.EnrollEdgeCredential(ctx, m.NodeID, "action-enrollment", "action-public-key", "session", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: m.ID + "-action-app", ProjectID: m.ProjectID, ServerID: m.ID, Name: "Action app", CreatedAt: now}
	if err = s.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	job := core.RuntimeJob{ID: m.ID + "-runtime", ServerID: m.ID, NodeID: m.NodeID, NodeGeneration: 1, ProjectID: m.ProjectID, AppID: app.ID, Operation: "inspect", RequestDigest: "runtime-digest", EncryptedRequest: "wrapped-runtime", ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err = s.CreateRuntimeJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.AcceptInfrastructureAction(ctx, desired, op, payload, now, nil); !errors.Is(err, ErrInfrastructureProtected) {
		t.Fatal("admitted runtime bypassed action fence", err)
	}
	if _, err = s.db.ExecContext(ctx, s.q(`UPDATE runtime_jobs SET state='succeeded' WHERE id=?`), job.ID); err != nil {
		t.Fatal(err)
	}
	m, accepted, err := s.AcceptInfrastructureAction(ctx, desired, op, payload, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.PowerState != "transitioning" || m.RuntimeState != "waiting" {
		t.Fatal("action didn't fence readiness", m)
	}
	saved, err := s.GetInfrastructureActionRequest(ctx, op.ID)
	if err != nil || saved != payload {
		t.Fatal("immutable ciphertext not persisted", saved, err)
	}
	replay, again, err := s.AcceptInfrastructureAction(ctx, desired, op, payload, now, nil)
	if err != nil || again.ID != accepted.ID || replay.Revision != m.Revision {
		t.Fatal("acceptance replay changed identity", again, err)
	}
	job.ID += "-late"
	if err = s.CreateRuntimeJob(ctx, job); err == nil {
		t.Fatal("accepted power operation admitted runtime work")
	}
	if err = s.CreateApp(ctx, core.App{ID: m.ID + "-late", ProjectID: m.ProjectID, ServerID: m.ID, Name: "Late app", CreatedAt: now}); err == nil {
		t.Fatal("accepted action admitted a new application")
	}
	lease, err := s.LeaseInfrastructureOperation(ctx, now, time.Minute)
	if err != nil || lease == nil || lease.ID != op.ID {
		t.Fatal("action lease", lease, err)
	}
	done := m
	done.PowerState = "stopped"
	done.Revision++
	lease.State = "succeeded"
	lease.Stage = "poll"
	lease.ProviderOperationID = "power-provider-operation"
	stale := *lease
	stale.LeaseToken = "retired-worker"
	if err = s.CompleteInfrastructureAction(ctx, done, stale, core.EdgeCredential{}, now, nil); !errors.Is(err, ErrInfrastructureChanged) {
		t.Fatal("stale action lease committed", err)
	}
	if err = s.CompleteInfrastructureAction(ctx, done, *lease, core.EdgeCredential{}, now, nil); err != nil {
		t.Fatal(err)
	}
	// Restore the target for the remainder of the shared lifecycle checks.
	m, err = s.GetManagedServer(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	m.PowerState = "running"
	m.RuntimeState = "ready"
	m.Revision++
	if err = s.UpdateManagedServer(ctx, m, m.Revision-1); err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshManagedTargetState(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	return m
}

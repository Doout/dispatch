package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func mutationFixture(suffix string) core.MutationReceipt {
	return core.MutationReceipt{ID: "mutation-" + suffix, CallerKind: "user", CallerID: "actor-" + suffix, ProjectID: "project-" + suffix, Action: "deployment.start", KeyDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("key"))), RequestDigest: fmt.Sprintf("%x", sha256.Sum256([]byte("request"))), OperationKind: "deployment", OperationID: "operation-" + suffix, ResourceID: "app-" + suffix, ClaimToken: "claim-" + suffix}
}
func mutationStore(t *testing.T, url string) *SQLStore {
	t.Helper()
	s, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err = s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s
}
func mutationClaim(r core.MutationReceipt) core.MutationAcceptance {
	return core.MutationAcceptance{ReceiptID: r.ID, ClaimToken: r.ClaimToken, OperationKind: r.OperationKind, OperationID: r.OperationID}
}

func TestMutationReceiptSQLite(t *testing.T) {
	testMutationReceipt(t, mutationStore(t, filepath.Join(t.TempDir(), "receipts.db")))
}
func TestMutationReceiptPostgres(t *testing.T) {
	url := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL to a disposable PostgreSQL database")
	}
	testMutationReceipt(t, mutationStore(t, url))
	testAtomicMutationAcceptance(t, url)
}
func testMutationReceipt(t *testing.T, s *SQLStore) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	r := mutationFixture(ulid.Make().String())
	// Concurrent retries reserve exactly one acceptance claim and one operation ID.
	var wg sync.WaitGroup
	var mu sync.Mutex
	claimed := 0
	var winner core.MutationReceipt
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := r
			candidate.OperationID = ulid.Make().String()
			candidate.ClaimToken = ulid.Make().String()
			saved, won, err := s.ReserveMutationReceipt(ctx, candidate, now)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				t.Error(err)
				return
			}
			if won {
				claimed++
				winner = saved
			}
		}()
	}
	wg.Wait()
	r.OperationID = winner.OperationID
	r.ClaimToken = winner.ClaimToken
	if claimed != 1 {
		t.Fatalf("claimed %d operations", claimed)
	}
	saved, err := s.GetMutationReceipt(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	changed := r
	changed.RequestDigest = fmt.Sprintf("%x", sha256.Sum256([]byte("different")))
	if _, _, err = s.ReserveMutationReceipt(ctx, changed, now); !errors.Is(err, ErrMutationConflict) {
		t.Fatalf("changed input accepted: %v", err)
	}
	// A crashed preparation can be reclaimed without changing its operation ID.
	next := r
	next.ClaimToken = "replacement"
	next.OperationID = "must-not-replace"
	saved, won, err := s.ReserveMutationReceipt(ctx, next, now.Add(MutationClaimDuration+time.Second))
	if err != nil || !won || saved.OperationID != r.OperationID {
		t.Fatalf("preparation recovery changed operation: %#v %t %v", saved, won, err)
	}
	// A stale preparer cannot commit any operation side effect after losing its lease.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindMutationAcceptance(core.WithMutationAcceptance(ctx, mutationClaim(r)), tx.Tx, "deployment", r.OperationID); !errors.Is(err, ErrMutationClaimLost) {
		t.Fatalf("stale preparer committed: %v", err)
	}
	tx.Rollback()
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindMutationAcceptance(core.WithMutationAcceptance(ctx, mutationClaim(saved)), tx.Tx, "deployment", saved.OperationID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	replay, won, err := s.ReserveMutationReceipt(ctx, next, now.Add(2*MutationClaimDuration))
	if err != nil || won || replay.State != "accepted" || replay.OperationID != saved.OperationID {
		t.Fatalf("accepted operation repeated: %#v %t %v", replay, won, err)
	}
	// Receipt identity remains after its bounded replay window; it is never reused.
	if _, _, err = s.ReserveMutationReceipt(ctx, next, now.Add(MutationRetryWindow)); !errors.Is(err, ErrMutationExpired) {
		t.Fatalf("expired retry was allowed: %v", err)
	}
	if _, err = s.GetMutationReceipt(ctx, r.ID); err != nil {
		t.Fatal("expired request identity was discarded")
	}
	// Another caller or project cannot reuse this scoped receipt ID.
	for _, field := range []string{"caller", "project"} {
		other := r
		if field == "caller" {
			other.CallerID = "other"
		} else {
			other.ProjectID = "other"
		}
		if _, _, err = s.ReserveMutationReceipt(ctx, other, now); !errors.Is(err, ErrMutationConflict) {
			t.Fatalf("cross-%s receipt exposed: %v", field, err)
		}
	}
}

func TestMutationReceiptRestartAndAtomicDeploymentAcceptance(t *testing.T) {
	testAtomicMutationAcceptance(t, filepath.Join(t.TempDir(), "receipts.db"))
}

func testAtomicMutationAcceptance(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	s := mutationStore(t, path)
	now := time.Now().UTC()
	r := mutationFixture(ulid.Make().String())
	serverID := "server-" + r.OperationID
	for _, err := range []error{s.CreateProject(ctx, core.Project{ID: r.ProjectID, Name: r.ProjectID, CreatedAt: now}), s.CreateServer(ctx, core.Server{ID: serverID, Name: serverID, Address: "local", CreatedAt: now}), s.CreateApp(ctx, core.App{ID: r.ResourceID, ProjectID: r.ProjectID, ServerID: serverID, Name: "App", CreatedAt: now})} {
		if err != nil {
			t.Fatal(err)
		}
	}
	saved, won, err := s.ReserveMutationReceipt(ctx, r, now)
	if err != nil || !won {
		t.Fatal(err)
	}
	claim := mutationClaim(saved)
	d := core.Deployment{ID: saved.OperationID, AppID: r.ResourceID, State: core.DeploymentQueued, CreatedAt: now}
	// A rejected capture rolls back both operation insertion and receipt acceptance.
	bad := d
	bad.AppID = "missing"
	if err = s.CreateDeployment(core.WithMutationAcceptance(ctx, claim), bad); err == nil {
		t.Fatal("invalid operation accepted")
	}
	receipt, _ := s.GetMutationReceipt(ctx, r.ID)
	if receipt.State != "reserved" {
		t.Fatal("rollback accepted receipt without operation")
	}
	if err = s.CreateDeployment(core.WithMutationAcceptance(ctx, claim), d); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = mutationStore(t, path)
	replay, won, err := s.ReserveMutationReceipt(ctx, r, now.Add(time.Second))
	if err != nil || won || replay.State != "accepted" {
		t.Fatalf("restart repeated acceptance: %#v %t %v", replay, won, err)
	}
	deployments, err := s.ListApplicationHistory(ctx, r.ResourceID, "", 20)
	if err != nil || len(deployments) != 1 || deployments[0].ID != d.ID {
		t.Fatalf("operation not atomic: %#v %v", deployments, err)
	}
	recovered, err := s.RecoverInterruptedDeployments(ctx, now.Add(2*time.Second), now.Add(time.Second))
	// A shared PostgreSQL database can contain other recoverable executions.
	owned := []core.Deployment{}
	for _, candidate := range recovered {
		if candidate.AppID == r.ResourceID {
			owned = append(owned, candidate)
		}
	}
	if err != nil || len(owned) != 1 || owned[0].ID != d.ID || owned[0].State != core.DeploymentFailed {
		t.Fatalf("accepted execution recovery: %#v %v", owned, err)
	}
	unresolved, won, err := s.ReserveMutationReceipt(ctx, r, now.Add(3*time.Second))
	if err != nil || won || unresolved.State != "unresolved" {
		t.Fatalf("uncertain operation was rescheduled: %#v %t %v", unresolved, won, err)
	}
	d.State = core.DeploymentSucceeded
	if err = s.UpdateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteApp(ctx, r.ResourceID); err != nil {
		t.Fatal(err)
	}
	final, won, err := s.ReserveMutationReceipt(ctx, r, now.Add(2*time.Second))
	if err != nil || won || final.State != "succeeded" {
		t.Fatalf("pruning lost accepted outcome: %#v %t %v", final, won, err)
	}
}

package backupoperations

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

type executionFixture struct {
	backup            core.WorkloadBackup
	operation         core.WorkloadBackupOperation
	input             core.WorkloadBackupRequest
	server            core.Server
	result            core.WorkloadBackupResult
	savedBackup       core.WorkloadBackup
	savedOperation    core.WorkloadBackupOperation
	completionContext context.Context
	dispatched        core.WorkloadBackupRequest
	executionDeadline time.Time
	decodeErr         error
	executionErr      error
	completionErr     error
	policyChecks      int
	generationChecks  int
	dispatches        int
	deletions         int
	grants            []objectGrant
	checkPolicy       func() error
	generationMatches func() bool
	lock              func(context.Context, func() error) error
	run               func(context.Context, core.WorkloadBackupRequest) (core.WorkloadBackupResult, error)
}

type objectGrant struct {
	storeID       string
	write, remove bool
}

func newExecutionFixture() *executionFixture {
	b := core.WorkloadBackup{ID: "original-archive", ProjectID: "project", State: "creating", ServerID: "server", NodeID: "node", EncryptedInput: "original-backup-cipher", Revision: 7}
	op := core.WorkloadBackupOperation{ID: "original-operation", BackupID: b.ID, ProjectID: b.ProjectID, Action: "backup", LeaseUntil: time.Now().Add(31 * time.Minute), LeaseToken: "original-lease", EncryptedInput: "original-operation-cipher", Revision: 4}
	return &executionFixture{
		backup: b, operation: op,
		input:  core.WorkloadBackupRequest{OperationID: op.ID, Action: op.Action, Key: "retained-key", Backup: b},
		server: core.Server{ID: b.ServerID, AgentNodeID: b.NodeID},
		result: core.WorkloadBackupResult{State: "ready", CleanupState: "complete", Checksum: "authenticated-checksum"},
	}
}

func (f *executionFixture) service() Service {
	return Service{Records: f, Authority: f, Material: f, Execution: f}
}

func (f *executionFixture) GetWorkloadBackup(ctx context.Context, id string) (core.WorkloadBackup, error) {
	if id != f.backup.ID {
		return core.WorkloadBackup{}, errors.New("wrong archive")
	}
	return f.backup, ctx.Err()
}

func (f *executionFixture) GetServer(ctx context.Context, id string) (core.Server, error) {
	if id != f.server.ID {
		return core.Server{}, errors.New("wrong target")
	}
	return f.server, ctx.Err()
}

func (f *executionFixture) CompleteWorkloadBackupOperation(ctx context.Context, b core.WorkloadBackup, op core.WorkloadBackupOperation) error {
	f.savedBackup, f.savedOperation, f.completionContext = b, op, ctx
	return f.completionErr
}

func (f *executionFixture) DecodeOperation(op core.WorkloadBackupOperation) (core.WorkloadBackupRequest, error) {
	if op.ID != f.operation.ID || op.EncryptedInput != f.operation.EncryptedInput {
		return core.WorkloadBackupRequest{}, errors.New("wrong encrypted operation")
	}
	return f.input, f.decodeErr
}

func (f *executionFixture) GrantObjects(_ context.Context, _ core.WorkloadBackup, storeID string, write, remove bool) (backupstore.Access, error) {
	f.grants = append(f.grants, objectGrant{storeID, write, remove})
	return backupstore.Access{}, nil
}

func (f *executionFixture) CheckPolicy(_ context.Context, _ core.WorkloadBackupOperation) error {
	f.policyChecks++
	if f.checkPolicy != nil {
		return f.checkPolicy()
	}
	return nil
}

func (f *executionFixture) ExecutionGenerationMatches(_ context.Context, _ core.WorkloadBackupOperation) bool {
	f.generationChecks++
	return f.generationMatches == nil || f.generationMatches()
}

func (f *executionFixture) WithTarget(ctx context.Context, _ string, fn func() error) error {
	if f.lock != nil {
		return f.lock(ctx, fn)
	}
	return fn()
}

func (f *executionFixture) Run(ctx context.Context, input core.WorkloadBackupRequest, _ core.Server) (core.WorkloadBackupResult, error) {
	f.dispatches++
	f.dispatched = input
	f.executionDeadline, _ = ctx.Deadline()
	if f.run != nil {
		return f.run(ctx, input)
	}
	return f.result, f.executionErr
}

func (f *executionFixture) DeleteOffsite(ctx context.Context, input core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
	f.deletions++
	f.dispatched = input
	return f.result, f.executionErr
}

func TestExecutionUsesAcceptedDeadlineAndPreservesLease(t *testing.T) {
	f := newExecutionFixture()
	f.operation.LeaseUntil = time.Now().Add(90 * time.Second)
	if err := f.service().Execute(context.Background(), f.operation, false); err != nil {
		t.Fatal(err)
	}
	if !f.executionDeadline.Equal(f.operation.LeaseUntil.Add(-time.Minute)) {
		t.Fatal("execution received a new deadline", f.executionDeadline)
	}
	if f.savedOperation.State != "succeeded" || f.savedBackup.State != "ready" || f.savedOperation.LeaseToken != f.operation.LeaseToken || !f.savedOperation.LeaseUntil.Equal(f.operation.LeaseUntil) || f.savedOperation.EncryptedInput != f.operation.EncryptedInput {
		t.Fatal("execution changed accepted identity or recovery lease", f.savedOperation)
	}
}

func TestDelayedExecutionLeavesExpiredWindowForRecovery(t *testing.T) {
	f := newExecutionFixture()
	f.operation.LeaseUntil = time.Now().Add(30 * time.Second)
	if err := f.service().Execute(context.Background(), f.operation, false); err != nil {
		t.Fatal(err)
	}
	if f.dispatches != 0 || f.policyChecks != 0 || f.completionContext != nil {
		t.Fatal("expired accepted window dispatched or replaced its durable outcome")
	}
}

func TestAuthorityRevokedWhileWaitingForTargetStopsDispatch(t *testing.T) {
	for _, boundary := range []string{"policy", "enrollment"} {
		t.Run(boundary, func(t *testing.T) {
			f := newExecutionFixture()
			f.operation.ExecutionServerID, f.operation.ExecutionNodeID = f.server.ID, f.server.AgentNodeID
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var revoked atomic.Bool
			f.lock = func(ctx context.Context, fn func() error) error {
				close(entered)
				select {
				case <-release:
					return fn()
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if boundary == "policy" {
				f.checkPolicy = func() error {
					if revoked.Load() {
						return errors.New("policy paused")
					}
					return nil
				}
			} else {
				f.generationMatches = func() bool { return !revoked.Load() }
				f.checkPolicy = func() error {
					if revoked.Load() {
						return errors.New("policy paused")
					}
					return nil
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() { done <- f.service().Execute(ctx, f.operation, false) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("execution did not reach target lock after its initial authority check")
			}
			revoked.Store(true)
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("execution did not finish after target lock release")
			}
			if f.dispatches != 0 || f.savedOperation.State != "failed" || f.savedOperation.CleanupState != "complete" || f.savedBackup.State != "failed" {
				t.Fatal("revoked authority dispatched or claimed a recovery point", f.savedOperation, f.savedBackup.State)
			}
			if boundary == "enrollment" && f.policyChecks != 1 {
				t.Fatal("a later policy check replaced the revoked enrollment outcome", f.policyChecks)
			}
		})
	}
}

func TestRejectedPolicyCannotRefreshGrantsOrDispatch(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		f := newExecutionFixture()
		f.input.OffsiteAccess = &backupstore.Access{}
		f.checkPolicy = func() error { return errors.New("policy paused") }
		if err := f.service().Execute(context.Background(), f.operation, recovering); err != nil {
			t.Fatal(err)
		}
		state, cleanup := "failed", "complete"
		if recovering {
			state, cleanup = "unknown", "pending"
		}
		if len(f.grants) != 0 || f.dispatches != 0 || f.generationChecks != 0 || f.savedOperation.State != state || f.savedOperation.CleanupState != cleanup {
			t.Fatal("revoked policy refreshed access or discarded an uncertain recovery", recovering, f.savedOperation)
		}
	}
}

func TestDecryptionFailureCannotCheckAuthorityOrDispatch(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		f := newExecutionFixture()
		f.decodeErr = errors.New("encrypted operation cannot be authenticated")
		if err := f.service().Execute(context.Background(), f.operation, recovering); err != nil {
			t.Fatal(err)
		}
		state, cleanup := "failed", "complete"
		if recovering {
			state, cleanup = "unknown", "pending"
		}
		if f.policyChecks != 0 || f.dispatches != 0 || f.savedOperation.State != state || f.savedOperation.CleanupState != cleanup || f.savedOperation.EncryptedInput != f.operation.EncryptedInput {
			t.Fatal("unreadable request lost its preparation or recovery classification", recovering, f.savedOperation)
		}
	}
}

func TestInterruptedDispatchPersistsUncertainOutcomeWithoutCancellation(t *testing.T) {
	f := newExecutionFixture()
	type contextKey struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "operation-observer"))
	defer cancel()
	f.run = func(context.Context, core.WorkloadBackupRequest) (core.WorkloadBackupResult, error) {
		cancel()
		return core.WorkloadBackupResult{State: "ready", CleanupState: "complete", Checksum: "unconfirmed"}, nil
	}
	if err := f.service().Execute(ctx, f.operation, false); err != nil {
		t.Fatal(err)
	}
	if f.savedOperation.State != "unknown" || f.savedBackup.State != "unknown" || f.savedBackup.Checksum != "" || f.savedOperation.EncryptedInput != f.operation.EncryptedInput || f.savedBackup.EncryptedInput != f.backup.EncryptedInput {
		t.Fatal("interrupted execution replaced retained recovery material or trusted an unconfirmed archive")
	}
	_, deadline := f.completionContext.Deadline()
	if f.completionContext.Err() != nil || deadline || f.completionContext.Value(contextKey{}) != "operation-observer" {
		t.Fatal("completion inherited cancellation or lost context values")
	}
}

func TestRecoveryInspectsOriginalExportWithoutWriteGrant(t *testing.T) {
	f := newExecutionFixture()
	f.backup.State = "ready"
	f.operation.Action, f.input.Action = "export", "export"
	f.operation.OffsiteStoreID = "accepted-object-store"
	f.input.OffsiteAccess = &backupstore.Access{}
	f.result.State = "unresolved"
	if err := f.service().Execute(context.Background(), f.operation, true); err != nil {
		t.Fatal(err)
	}
	if f.dispatched.OperationID != f.operation.ID || f.dispatched.Action != "reconcile" || f.dispatched.RecoveryAction != "export" || f.dispatched.Key != f.input.Key || len(f.grants) != 1 || f.grants[0] != (objectGrant{storeID: f.operation.OffsiteStoreID}) {
		t.Fatal("recovery recreated an export or changed its accepted identity", f.dispatched, f.grants)
	}
	if f.savedOperation.State != "unresolved" || f.savedBackup.State != "ready" || f.savedOperation.LeaseToken != f.operation.LeaseToken {
		t.Fatal("original export inspection discarded recovery evidence", f.savedOperation)
	}
}

func TestUnresolvedResultOnlyClosesARecoveryInspection(t *testing.T) {
	for _, recovering := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			f := newExecutionFixture()
			f.result.State = "unresolved"
			if failed {
				f.executionErr = errors.New("inspection reply lost")
			}
			if err := f.service().Execute(context.Background(), f.operation, recovering); err != nil {
				t.Fatal(err)
			}
			state := "unknown"
			if recovering && !failed {
				state = "unresolved"
			}
			if f.savedOperation.State != state || f.savedBackup.State != "unknown" {
				t.Fatal("unconfirmed inspection released its original recovery state", recovering, failed, f.savedOperation.State)
			}
		}
	}
}

func TestUncertainOffsiteDeletionRetainsProtection(t *testing.T) {
	f := newExecutionFixture()
	f.backup.State, f.backup.LocalState = "ready", "retired"
	f.backup.Offsite = &core.BackupOffsiteArtifact{StoreID: "accepted-object-store", VerificationState: "verified"}
	f.operation.Action, f.input.Action = "delete-offsite", "delete-offsite"
	f.operation.OffsiteStoreID = f.backup.Offsite.StoreID
	f.executionErr = &runtimecontract.Error{Code: runtimecontract.Uncertain, Message: "reply lost"}
	f.result.State = "offsite-deleted"
	if err := f.service().Execute(context.Background(), f.operation, false); err != nil {
		t.Fatal(err)
	}
	if f.dispatches != 0 || f.deletions != 1 || len(f.grants) != 1 || f.grants[0] != (objectGrant{storeID: f.operation.OffsiteStoreID, remove: true}) {
		t.Fatal("offsite deletion changed its execution path or grant", f.grants)
	}
	if f.savedOperation.State != "unknown" || f.savedBackup.State != "ready" || f.savedBackup.Offsite.DeletedAt != nil || f.savedBackup.Offsite.VerificationState != "deletion-pending" || f.backup.Offsite.VerificationState != "verified" {
		t.Fatal("uncertain deletion released the last recovery copy or mutated the loaded record")
	}
}

func TestVerificationCleanupFailureRetainsUncertainty(t *testing.T) {
	f := newExecutionFixture()
	f.backup.State = "ready"
	f.backup.Offsite = &core.BackupOffsiteArtifact{VerificationState: "not_verified"}
	f.input.OffsiteAccess = &backupstore.Access{}
	f.operation.Action, f.input.Action = "verify", "verify"
	f.result.State, f.result.CleanupState = "verified", "failed"
	f.result.Message = "verification container remains"
	if err := f.service().Execute(context.Background(), f.operation, false); err != nil {
		t.Fatal(err)
	}
	if f.savedOperation.State != "unknown" || f.savedBackup.VerificationState != "unknown" || f.savedBackup.VerifiedAt != nil || f.savedBackup.Offsite.VerifiedAt != nil || f.savedBackup.CleanupState != "failed" {
		t.Fatal("verification trusted a recovery point before cleanup was confirmed", f.savedOperation, f.savedBackup)
	}
}

func TestCompletionFailureReturnsToCallerWithoutRetryingExecution(t *testing.T) {
	f := newExecutionFixture()
	f.completionErr = errors.New("outcome write failed")
	err := f.service().Execute(context.Background(), f.operation, false)
	if !errors.Is(err, f.completionErr) || f.dispatches != 1 || f.savedOperation.State != "succeeded" || strings.Contains(f.savedOperation.Message, f.completionErr.Error()) {
		t.Fatal("completion failure repeated execution or replaced its observed outcome", err, f.dispatches)
	}
}

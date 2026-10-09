package backupoperations

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type recoveryFixture struct {
	now                                            time.Time
	policy                                         core.WorkloadBackupPolicy
	backups                                        []core.WorkloadBackup
	operations                                     map[string][]core.WorkloadBackupOperation
	trace                                          []string
	claims                                         []string
	dispatched                                     []core.WorkloadBackupOperation
	authorityErr, listErr, operationsErr, claimErr error
}

func newRecoveryFixture() *recoveryFixture {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	p := core.WorkloadBackupPolicy{ID: "policy", ProjectID: "project"}
	return &recoveryFixture{now: now, policy: p,
		backups:    []core.WorkloadBackup{{ID: "original-archive", CapturePolicyID: p.ID}},
		operations: map[string][]core.WorkloadBackupOperation{"original-archive": {{ID: "original-operation", BackupID: "original-archive", CapturePolicyID: p.ID, State: "unknown", LeaseUntil: now.Add(-time.Second), EncryptedInput: "original-cipher", LeaseToken: "original-token", Revision: 7}}},
	}
}

func (f *recoveryFixture) CheckCapturePolicy(context.Context, core.WorkloadBackupPolicy) error {
	f.trace = append(f.trace, "authority")
	return f.authorityErr
}
func (f *recoveryFixture) ListWorkloadBackups(_ context.Context, project string) ([]core.WorkloadBackup, error) {
	f.trace = append(f.trace, "backups:"+project)
	return f.backups, f.listErr
}
func (f *recoveryFixture) ListWorkloadBackupOperations(_ context.Context, id string) ([]core.WorkloadBackupOperation, error) {
	f.trace = append(f.trace, "operations:"+id)
	return f.operations[id], f.operationsErr
}
func (f *recoveryFixture) ClaimWorkloadBackupRecovery(_ context.Context, id string, now time.Time, token string) (core.WorkloadBackupOperation, error) {
	f.trace = append(f.trace, "claim:"+id)
	f.claims = append(f.claims, id)
	for _, ops := range f.operations {
		for _, op := range ops {
			if op.ID == id {
				op.LeaseToken, op.LeaseUntil, op.State = token, now.Add(31*time.Minute), "running"
				op.Revision++
				return op, f.claimErr
			}
		}
	}
	return core.WorkloadBackupOperation{}, store.ErrNotFound
}
func (f *recoveryFixture) Dispatch(op core.WorkloadBackupOperation, recovering bool) {
	if !recovering {
		panic("recovery scheduled a fresh mutation")
	}
	f.trace = append(f.trace, "dispatch:"+op.ID)
	f.dispatched = append(f.dispatched, op)
}
func (f *recoveryFixture) recover() {
	RecoveryScheduler{Records: f, Authority: f, Dispatch: f, Now: func() time.Time { return f.now }, NewToken: func() string { return "replacement-token" }}.Recover(context.Background(), f.policy)
}

func TestRecoverySkipsActiveLeasesAndClosedOperations(t *testing.T) {
	f := newRecoveryFixture()
	original := f.operations["original-archive"][0]
	f.backups = append([]core.WorkloadBackup{{ID: "foreign-archive", CapturePolicyID: "other-policy"}}, f.backups...)
	ops := []core.WorkloadBackupOperation{}
	for _, state := range []string{"succeeded", "failed", "unresolved"} {
		op := original
		op.ID, op.State = state, state
		ops = append(ops, op)
	}
	foreign := original
	foreign.ID, foreign.CapturePolicyID = "foreign", "other-policy"
	active := original
	active.ID, active.LeaseUntil = "active", f.now.Add(time.Nanosecond)
	second := original
	second.ID = "second-expired-operation"
	ops = append(ops, foreign, active, original, second)
	f.operations["original-archive"] = ops
	f.recover()
	if !reflect.DeepEqual(f.claims, []string{original.ID}) || len(f.dispatched) != 1 {
		t.Fatal("recovery claimed an ineligible operation", f.claims, len(f.dispatched))
	}
	claimed := f.dispatched[0]
	if claimed.ID != original.ID || claimed.BackupID != original.BackupID || claimed.EncryptedInput != original.EncryptedInput || claimed.LeaseToken != "replacement-token" || claimed.Revision != original.Revision+1 || !claimed.LeaseUntil.Equal(f.now.Add(31*time.Minute)) {
		t.Fatal("recovery changed accepted identity or discarded recovery material")
	}
}

func TestRecoveryAcceptsBothUncertainStatesAtLeaseBoundary(t *testing.T) {
	for _, state := range []string{"running", "unknown"} {
		t.Run(state, func(t *testing.T) {
			f := newRecoveryFixture()
			op := f.operations["original-archive"][0]
			op.State, op.LeaseUntil = state, f.now
			f.operations["original-archive"] = []core.WorkloadBackupOperation{op}
			f.recover()
			if len(f.claims) != 1 || len(f.dispatched) != 1 {
				t.Fatal("lease equality blocked eligible recovery", f.claims, len(f.dispatched))
			}
		})
	}
}

func TestRecoveryRevocationStopsBeforeReadingArchives(t *testing.T) {
	f := newRecoveryFixture()
	f.authorityErr = store.ErrAutomationCredential
	f.recover()
	if !reflect.DeepEqual(f.trace, []string{"authority"}) {
		t.Fatal("revoked authority read or claimed recovery material", f.trace)
	}
}

func TestRecoveryStopsAfterOneFailedClaim(t *testing.T) {
	f := newRecoveryFixture()
	second := f.operations["original-archive"][0]
	second.ID = "second-operation"
	f.operations["original-archive"] = append(f.operations["original-archive"], second)
	f.backups = append(f.backups, core.WorkloadBackup{ID: "second-archive", CapturePolicyID: f.policy.ID})
	f.operations["second-archive"] = []core.WorkloadBackupOperation{second}
	f.claimErr = store.ErrWorkloadBackupChanged
	f.recover()
	if !reflect.DeepEqual(f.claims, []string{"original-operation"}) || len(f.dispatched) != 0 || !reflect.DeepEqual(f.trace, []string{"authority", "backups:project", "operations:original-archive", "claim:original-operation"}) {
		t.Fatal("failed claim advanced to another stale candidate", f.trace)
	}
}

func TestRecoveryReadFailuresKeepOriginalAttemptOrder(t *testing.T) {
	for _, stage := range []string{"backups", "operations"} {
		t.Run(stage, func(t *testing.T) {
			f := newRecoveryFixture()
			if stage == "backups" {
				f.listErr = errors.New("list failed")
			} else {
				f.operationsErr = errors.New("list failed")
			}
			f.recover()
			if len(f.claims) != 0 || len(f.dispatched) != 0 || f.trace[0] != "authority" || f.trace[1] != "backups:project" {
				t.Fatal("read failure changed recovery ordering", f.trace)
			}
		})
	}
}

type concurrentRecoveryRecords struct {
	*store.SQLStore
	arrived chan struct{}
	release chan struct{}
}

func (s concurrentRecoveryRecords) ListWorkloadBackupOperations(ctx context.Context, id string) ([]core.WorkloadBackupOperation, error) {
	ops, err := s.SQLStore.ListWorkloadBackupOperations(ctx, id)
	if err != nil {
		return ops, err
	}
	s.arrived <- struct{}{}
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return ops, nil
}

type currentPolicyAuthority struct{}

func (currentPolicyAuthority) CheckCapturePolicy(context.Context, core.WorkloadBackupPolicy) error {
	return nil
}

type recoveryDispatchRecord struct {
	op         core.WorkloadBackupOperation
	recovering bool
}

type recoveryChannel chan recoveryDispatchRecord

func (c recoveryChannel) Dispatch(op core.WorkloadBackupOperation, recovering bool) {
	c <- recoveryDispatchRecord{op: op, recovering: recovering}
}

func TestCompetingRecoveryTicksClaimOriginalOperationOnce(t *testing.T) {
	ctx := t.Context()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	p := core.WorkloadBackupPolicy{ID: "policy", ProjectID: "project"}
	for _, err := range []error{
		data.CreateProject(ctx, core.Project{ID: p.ProjectID, Name: "project", CreatedAt: now}),
		data.CreateServer(ctx, core.Server{ID: "server", Name: "server", Address: "local", Runtime: "docker", CreatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	run := core.ServiceProvisionRun{ID: "source", TemplateID: "template", ProjectID: p.ProjectID, ServiceName: "database", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: "server", ResourceName: "database"}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: p.ProjectID, ServiceID: "service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Revision: 1, OperationID: run.ID, Policy: "retain", EncryptedRequest: "source-cipher", CreatedAt: now, UpdatedAt: now}
	if err = data.CreateServiceResource(ctx, run, resource); err != nil {
		t.Fatal(err)
	}
	resource, err = data.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "initial", now, "")
	if err != nil {
		t.Fatal(err)
	}
	resource.State, resource.ResourceID = "ready", "owned-container"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	connection := core.Service{ID: resource.ServiceID, ProjectID: p.ProjectID, Name: run.ServiceName, Type: "postgresql", ProvisionRunID: run.ID, ProvisionTarget: run.Target, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = data.SaveServiceResource(ctx, resource, run, &connection); err != nil {
		t.Fatal(err)
	}
	b := core.WorkloadBackup{ID: "original-archive", CapturePolicyID: p.ID, ProjectID: p.ProjectID, ServerID: "server", SourceRunID: run.ID, State: "creating", Revision: 1, Policy: "retain", Location: "target-local", EncryptedInput: "archive-cipher", CreatedAt: now, UpdatedAt: now}
	op := NewOperation("original-operation", b, "backup")
	op.CapturePolicyID, op.EncryptedInput = p.ID, "operation-cipher"
	if err = data.CreateWorkloadBackup(ctx, b, op); err != nil {
		t.Fatal(err)
	}

	records := concurrentRecoveryRecords{SQLStore: data, arrived: make(chan struct{}, 2), release: make(chan struct{})}
	dispatches := make(recoveryChannel, 2)
	scheduler := RecoveryScheduler{Records: records, Authority: currentPolicyAuthority{}, Dispatch: dispatches, Now: func() time.Time { return now.Add(32 * time.Minute) }}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	workers.Add(2)
	for range 2 {
		go func() { defer workers.Done(); scheduler.Recover(ctx, p) }()
	}
	for range 2 {
		select {
		case <-records.arrived:
		case <-ctx.Done():
			close(records.release)
			workers.Wait()
			t.Fatal("competing ticks did not observe the original operation")
		}
	}
	close(records.release)
	workers.Wait()
	if len(dispatches) != 1 {
		t.Fatal("competing ticks dispatched duplicate inspection", len(dispatches))
	}
	dispatched := <-dispatches
	if !dispatched.recovering || dispatched.op.ID != op.ID || dispatched.op.BackupID != b.ID || dispatched.op.EncryptedInput != op.EncryptedInput || dispatched.op.LeaseToken == op.LeaseToken || dispatched.op.Revision != op.Revision+1 {
		t.Fatal("claim changed original inspection identity or recovery cipher")
	}
	saved, err := data.GetWorkloadBackup(ctx, b.ID)
	if err != nil || saved.EncryptedInput != b.EncryptedInput {
		t.Fatal("claim discarded archive recovery material", err)
	}
	op.State = "succeeded"
	if err = data.CompleteWorkloadBackupOperation(ctx, saved, op); err == nil {
		t.Fatal("stale worker overwrote replacement recovery claim", err)
	}
	current, err := data.GetWorkloadBackupOperation(ctx, op.ID)
	if err != nil || current.Revision != dispatched.op.Revision || current.LeaseToken != dispatched.op.LeaseToken || current.State != "running" || current.EncryptedInput != op.EncryptedInput {
		t.Fatal("rejected stale completion changed the claimed operation", err)
	}
}

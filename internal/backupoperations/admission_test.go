package backupoperations

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type encodedCapture struct {
	id, kind string
	input    core.WorkloadBackupRequest
}

type admissionFixture struct {
	source                                                       Source
	stored                                                       core.WorkloadBackupRequest
	now                                                          time.Time
	policy                                                       core.WorkloadBackupPolicy
	trace                                                        []string
	encoded                                                      []encodedCapture
	backup                                                       core.WorkloadBackup
	operation                                                    core.WorkloadBackupOperation
	acceptedPolicy                                               core.WorkloadBackupPolicy
	ids, keys, manualWrites, scheduledWrites, dispatches         int
	authorityErr, sourceErr, keyErr, validationErr, admissionErr error
	encodeFailure                                                string
	lock                                                         func(context.Context, func() error) error
}

func newAdmissionFixture() *admissionFixture {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	source := Source{Resource: core.ServiceResource{RunID: "source", ProjectID: "project", ResourceID: "owned-container"},
		Request: core.ServiceProvisionRequest{Password: "fresh-password", ServiceType: "postgresql"},
		Server:  core.Server{ID: "server", AgentNodeID: "node"},
		Storage: core.StorageResource{ID: "volume", Identity: "owned-volume", Evidence: "ownership-labels"},
	}
	return &admissionFixture{source: source, now: now,
		policy: core.WorkloadBackupPolicy{ID: "policy", ProjectID: "project", SourceRunID: "source", SourceResourceID: "owned-container", ServerID: "server", NodeID: "node", Enabled: true, IntervalHours: 1, NextCaptureAt: now.Add(-4 * time.Hour), Revision: 3},
		stored: core.WorkloadBackupRequest{Storage: source.Storage, Key: "policy-key-must-not-recur", Checks: []core.BackupIntegrityCheck{{Query: "SELECT 1", Expected: "1"}}},
	}
}

func (f *admissionFixture) service() Admission {
	return Admission{Sources: f, Material: f, Execution: f, Records: f, Policies: f, Authority: f, Dispatch: f,
		Now: func() time.Time { return f.now }, NewID: func() string {
			f.ids++
			if f.ids == 1 {
				return "fresh-archive"
			}
			return "fresh-lease"
		},
	}
}

func (f *admissionFixture) LoadSource(_ context.Context, id string) (Source, error) {
	f.trace = append(f.trace, "source:"+id)
	return f.source, f.sourceErr
}
func (f *admissionFixture) NewKey() (string, error) {
	f.trace = append(f.trace, "key")
	f.keys++
	return "fresh-archive-key", f.keyErr
}
func (f *admissionFixture) EncodeCapture(id, kind string, input core.WorkloadBackupRequest) (string, error) {
	f.trace = append(f.trace, "encode:"+kind)
	f.encoded = append(f.encoded, encodedCapture{id: id, kind: kind, input: input})
	if f.encodeFailure == kind {
		return "", errors.New("encryption failed")
	}
	return "encrypted:" + id + ":" + kind, nil
}
func (f *admissionFixture) DecodePolicy(core.WorkloadBackupPolicy) (core.WorkloadBackupRequest, error) {
	f.trace = append(f.trace, "decode-policy")
	return f.stored, nil
}
func (f *admissionFixture) WithTarget(ctx context.Context, id string, fn func() error) error {
	f.trace = append(f.trace, "lock:"+id)
	if f.lock != nil {
		return f.lock(ctx, fn)
	}
	return fn()
}
func (f *admissionFixture) ValidateCapture(core.WorkloadBackupRequest, core.Server) error {
	f.trace = append(f.trace, "validate")
	return f.validationErr
}
func (f *admissionFixture) CheckCapturePolicy(context.Context, core.WorkloadBackupPolicy) error {
	f.trace = append(f.trace, "authority")
	return f.authorityErr
}
func (f *admissionFixture) CreateWorkloadBackup(_ context.Context, b core.WorkloadBackup, op core.WorkloadBackupOperation) error {
	f.trace = append(f.trace, "manual-write")
	f.manualWrites++
	f.backup, f.operation = b, op
	return f.admissionErr
}
func (f *admissionFixture) AcceptWorkloadBackupCapture(_ context.Context, p core.WorkloadBackupPolicy, b core.WorkloadBackup, op core.WorkloadBackupOperation) error {
	f.trace = append(f.trace, "scheduled-write")
	f.scheduledWrites++
	f.acceptedPolicy, f.backup, f.operation = p, b, op
	return f.admissionErr
}
func (f *admissionFixture) Dispatch(core.WorkloadBackupOperation, bool) {
	f.trace = append(f.trace, "dispatch")
	f.dispatches++
}

func TestManualCaptureReadsFreshSourceAfterTargetWait(t *testing.T) {
	f := newAdmissionFixture()
	locked, release := make(chan struct{}), make(chan struct{})
	var sourceReads atomic.Int64
	f.lock = func(ctx context.Context, fn func() error) error {
		close(locked)
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
		sourceReads.Add(1)
		return fn()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := f.service().Manual(ctx, ManualCapture{ID: "receipt-operation", SourceRunID: "source", ServerID: "server", VerificationIntervalHours: 72, Checks: f.stored.Checks})
		done <- err
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("manual capture never waited for target")
	}
	if sourceReads.Load() != 0 {
		t.Fatal("source read before target was acquired")
	}
	f.source.Request.Password = "updated-during-target-wait"
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if f.manualWrites != 1 || f.scheduledWrites != 0 || f.dispatches != 0 || f.backup.ID != "receipt-operation" || f.operation.ID != f.backup.ID || f.backup.VerificationIntervalHours != 72 || f.backup.CapturePolicyID != "" || f.backup.ScheduledAt != nil {
		t.Fatal("manual admission changed receipt or schedule semantics")
	}
	if len(f.encoded) != 2 || f.encoded[0].input.Source.Password != "updated-during-target-wait" || f.encoded[1].input.Key != "fresh-archive-key" || f.encoded[0].id != f.backup.ID || f.encoded[0].kind != "accepted" || f.encoded[1].id != f.operation.ID || f.encoded[1].kind != "operation" {
		t.Fatal("manual capture retained stale source or wrong encryption binding")
	}
	if f.operation.Revision != 1 || f.operation.LeaseToken == "" || !f.operation.LeaseUntil.Equal(f.now.Add(31*time.Minute)) || f.backup.EncryptedInput == "" || f.operation.EncryptedInput == "" {
		t.Fatal("manual admission lost durable input or original execution window")
	}
}

func TestScheduledCaptureRevocationDuringTargetWaitStopsAdmission(t *testing.T) {
	f := newAdmissionFixture()
	locked, release := make(chan struct{}), make(chan struct{})
	f.lock = func(ctx context.Context, fn func() error) error {
		close(locked)
		select {
		case <-release:
			return fn()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.service().Scheduled(ctx, f.policy) }()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("scheduled capture never waited for target")
	}
	f.authorityErr = store.ErrAutomationCredential
	close(release)
	if err := <-done; !errors.Is(err, store.ErrAutomationCredential) {
		t.Fatal("revocation did not reject admission", err)
	}
	if f.ids != 0 || f.keys != 0 || f.scheduledWrites != 0 || f.dispatches != 0 || !reflect.DeepEqual(f.trace, []string{"decode-policy", "lock:server", "authority"}) {
		t.Fatal("revoked capture read source or constructed a new archive", f.trace)
	}
}

func TestScheduledCaptureUsesFreshKeyAndCoalescedSlot(t *testing.T) {
	f := newAdmissionFixture()
	if err := f.service().Scheduled(context.Background(), f.policy); err != nil {
		t.Fatal(err)
	}
	slot, _, _ := f.policy.DueCapture(f.now)
	if f.scheduledWrites != 1 || f.manualWrites != 0 || f.dispatches != 1 || f.backup.ID != "fresh-archive" || f.backup.ID == f.policy.ID || f.backup.CapturePolicyID != f.policy.ID || f.operation.CapturePolicyID != f.policy.ID || f.backup.ScheduledAt == nil || !f.backup.ScheduledAt.Equal(slot) || f.backup.VerificationIntervalHours != 24 || f.acceptedPolicy.Revision != f.policy.Revision {
		t.Fatal("scheduled admission changed fresh archive, slot or atomic policy guard")
	}
	if f.keys != 1 || len(f.encoded) != 2 || f.encoded[0].input.Key == f.stored.Key || f.encoded[0].input.Key != f.encoded[1].input.Key || !reflect.DeepEqual(f.encoded[0].input.Checks, f.stored.Checks) || f.operation.LeaseToken != "fresh-lease" {
		t.Fatal("scheduled capture reused policy key or lost accepted integrity checks")
	}
	if !reflect.DeepEqual(f.trace, []string{"decode-policy", "lock:server", "authority", "source:source", "key", "validate", "encode:accepted", "encode:operation", "scheduled-write", "dispatch"}) {
		t.Fatal("scheduled admission changed authority, encryption or dispatch order", f.trace)
	}
}

func TestScheduledCaptureRejectsChangedSourceBeforeGeneratingArchive(t *testing.T) {
	for _, change := range []string{"resource", "storage-id", "storage-identity", "storage-evidence", "not-due"} {
		t.Run(change, func(t *testing.T) {
			f := newAdmissionFixture()
			switch change {
			case "resource":
				f.source.Resource.ResourceID = "replacement-container"
			case "storage-id":
				f.source.Storage.ID = "replacement-volume"
			case "storage-identity":
				f.source.Storage.Identity = "replacement-volume"
			case "storage-evidence":
				f.source.Storage.Evidence = "replacement-labels"
			case "not-due":
				f.policy.NextCaptureAt = f.now.Add(time.Hour)
			}
			if err := f.service().Scheduled(context.Background(), f.policy); !errors.Is(err, store.ErrWorkloadBackupChanged) {
				t.Fatal("changed source admitted", err)
			}
			if f.ids != 0 || f.keys != 0 || len(f.encoded) != 0 || f.scheduledWrites != 0 || f.dispatches != 0 {
				t.Fatal("changed source generated or persisted fresh recovery material")
			}
		})
	}
}

func TestCaptureEncryptionAndAtomicAdmissionFailuresCannotDispatch(t *testing.T) {
	for _, kind := range []string{"accepted", "operation", "atomic-admission"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmissionFixture()
			if kind == "atomic-admission" {
				f.admissionErr = store.ErrWorkloadBackupChanged
			} else {
				f.encodeFailure = kind
			}
			if err := f.service().Scheduled(context.Background(), f.policy); err == nil {
				t.Fatal("failed encryption or atomic admission accepted")
			}
			if f.dispatches != 0 || f.manualWrites != 0 || kind != "atomic-admission" && f.scheduledWrites != 0 {
				t.Fatal("failed capture persisted or dispatched an operation")
			}
		})
	}
}

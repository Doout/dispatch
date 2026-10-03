package backupoperations

import (
	"context"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

// Source contains the authenticated accepted request and freshly observed storage.
// Callers load it while holding the original target lock.
type Source struct {
	Resource core.ServiceResource
	Request  core.ServiceProvisionRequest
	Server   core.Server
	Storage  core.StorageResource
}

type Sources interface {
	LoadSource(context.Context, string) (Source, error)
}

type CaptureMaterial interface {
	NewKey() (string, error)
	EncodeCapture(string, string, core.WorkloadBackupRequest) (string, error)
	DecodePolicy(core.WorkloadBackupPolicy) (core.WorkloadBackupRequest, error)
}

type CaptureExecution interface {
	WithTarget(context.Context, string, func() error) error
	ValidateCapture(core.WorkloadBackupRequest, core.Server) error
}

type CaptureRecords interface {
	CreateWorkloadBackup(context.Context, core.WorkloadBackup, core.WorkloadBackupOperation) error
}

type ScheduledCaptureRecords interface {
	AcceptWorkloadBackupCapture(context.Context, core.WorkloadBackupPolicy, core.WorkloadBackup, core.WorkloadBackupOperation) error
}

type PolicyAuthority interface {
	CheckCapturePolicy(context.Context, core.WorkloadBackupPolicy) error
}

type Dispatcher interface {
	Dispatch(core.WorkloadBackupOperation, bool)
}

// Admission creates new archives. Manual requests retain the caller's receipt ID;
// scheduled requests create a fresh ID only after checking authority and the due slot.
// Persistence adapters own the different atomic admission rules for each path.
type Admission struct {
	Sources   Sources
	Material  CaptureMaterial
	Execution CaptureExecution
	Records   CaptureRecords
	Policies  ScheduledCaptureRecords
	Authority PolicyAuthority
	Dispatch  Dispatcher
	Now       func() time.Time
	NewID     func() string
}

type ManualCapture struct {
	ID                        string
	SourceRunID               string
	ServerID                  string
	Checks                    []core.BackupIntegrityCheck
	VerificationIntervalHours int
}

func (s Admission) Manual(ctx context.Context, input ManualCapture) (core.WorkloadBackupOperation, error) {
	var op core.WorkloadBackupOperation
	err := s.Execution.WithTarget(ctx, input.ServerID, func() error {
		source, err := s.Sources.LoadSource(ctx, input.SourceRunID)
		if err != nil {
			return err
		}
		key, err := s.Material.NewKey()
		if err != nil {
			return err
		}
		b := newCapture(input.ID, source, input.Checks, input.VerificationIntervalHours, currentTime(s.Now))
		b, op, err = s.prepare(b, source, key, input.Checks)
		if err != nil {
			return err
		}
		return s.Records.CreateWorkloadBackup(ctx, b, op)
	})
	return op, err
}

func (s Admission) Scheduled(ctx context.Context, p core.WorkloadBackupPolicy) error {
	stored, err := s.Material.DecodePolicy(p)
	if err != nil {
		return err
	}
	return s.Execution.WithTarget(ctx, p.ServerID, func() error {
		if err := s.Authority.CheckCapturePolicy(ctx, p); err != nil {
			return err
		}
		source, err := s.Sources.LoadSource(ctx, p.SourceRunID)
		if err != nil {
			return err
		}
		if source.Resource.ResourceID != p.SourceResourceID || source.Storage.ID != stored.Storage.ID || source.Storage.Identity != stored.Storage.Identity || source.Storage.Evidence != stored.Storage.Evidence {
			return store.ErrWorkloadBackupChanged
		}
		now := currentTime(s.Now)
		slot, _, _ := p.DueCapture(now)
		if slot.IsZero() {
			return store.ErrWorkloadBackupChanged
		}
		id := operationID(s.NewID)
		key, err := s.Material.NewKey()
		if err != nil {
			return err
		}
		b := newCapture(id, source, stored.Checks, 24, now)
		b.CapturePolicyID, b.ScheduledAt = p.ID, &slot
		b.ProjectID, b.SourceRunID, b.NodeID, b.SourceResourceID = p.ProjectID, p.SourceRunID, p.NodeID, p.SourceResourceID
		b, op, err := s.prepare(b, source, key, stored.Checks)
		if err != nil {
			return err
		}
		if err = s.Policies.AcceptWorkloadBackupCapture(ctx, p, b, op); err != nil {
			return err
		}
		s.Dispatch.Dispatch(op, false)
		return nil
	})
}

func newCapture(id string, source Source, checks []core.BackupIntegrityCheck, interval int, now time.Time) core.WorkloadBackup {
	return core.WorkloadBackup{ID: id, ProjectID: source.Resource.ProjectID, SourceRunID: source.Resource.RunID, StorageID: source.Storage.ID, ServerID: source.Server.ID, NodeID: source.Server.AgentNodeID, SourceResourceID: source.Resource.ResourceID, State: "creating", Revision: 1, ArtifactID: id, Consistency: "database-native", Format: "postgresql-custom", Encryption: "AES-256-GCM-chunks-v1", KeyID: id, Location: "target-local", Policy: "retain", CheckCount: len(checks), VerificationState: "not_verified", CleanupState: "complete", VerificationIntervalHours: interval, CreatedAt: now, UpdatedAt: now}
}

func (s Admission) prepare(b core.WorkloadBackup, source Source, key string, checks []core.BackupIntegrityCheck) (core.WorkloadBackup, core.WorkloadBackupOperation, error) {
	var op core.WorkloadBackupOperation
	request := core.WorkloadBackupRequest{OperationID: b.ID, Action: "backup", Backup: b, Source: source.Request, Storage: source.Storage, Key: key, Checks: checks}
	if err := s.Execution.ValidateCapture(request, source.Server); err != nil {
		return b, op, err
	}
	var err error
	b.EncryptedInput, err = s.Material.EncodeCapture(b.ID, "accepted", request)
	if err != nil {
		return b, op, err
	}
	op = newOperation(b.ID, b, "backup", currentTime(s.Now), operationID(s.NewID))
	op.CapturePolicyID = b.CapturePolicyID
	op.EncryptedInput, err = s.Material.EncodeCapture(op.ID, "operation", request)
	return b, op, err
}

func NewOperation(id string, b core.WorkloadBackup, action string) core.WorkloadBackupOperation {
	return newOperation(id, b, action, currentTime(nil), operationID(nil))
}

func newOperation(id string, b core.WorkloadBackup, action string, now time.Time, token string) core.WorkloadBackupOperation {
	return core.WorkloadBackupOperation{ID: id, BackupID: b.ID, ProjectID: b.ProjectID, Action: action, State: "running", Revision: 1, LeaseToken: token, LeaseUntil: now.Add(31 * time.Minute), CreatedAt: now, UpdatedAt: now}
}

func currentTime(now func() time.Time) time.Time {
	if now != nil {
		return now().UTC()
	}
	return time.Now().UTC()
}

func operationID(newID func() string) string {
	if newID != nil {
		return newID()
	}
	return ulid.Make().String()
}

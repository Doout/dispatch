package backupoperations

import (
	"context"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type RecoveryRecords interface {
	ListWorkloadBackups(context.Context, string) ([]core.WorkloadBackup, error)
	ListWorkloadBackupOperations(context.Context, string) ([]core.WorkloadBackupOperation, error)
	ClaimWorkloadBackupRecovery(context.Context, string, time.Time, string) (core.WorkloadBackupOperation, error)
}

// RecoveryScheduler inspects at most one expired operation for a policy per tick.
// A rejected claim also consumes that attempt, so competing ticks cannot advance
// to a second candidate based on the same stale list.
type RecoveryScheduler struct {
	Records   RecoveryRecords
	Authority PolicyAuthority
	Dispatch  Dispatcher
	Now       func() time.Time
	NewToken  func() string
}

func (s RecoveryScheduler) Recover(ctx context.Context, p core.WorkloadBackupPolicy) {
	if s.Authority.CheckCapturePolicy(ctx, p) != nil {
		return
	}
	items, err := s.Records.ListWorkloadBackups(ctx, p.ProjectID)
	if err != nil {
		return
	}
	for _, b := range items {
		if b.CapturePolicyID != p.ID {
			continue
		}
		ops, err := s.Records.ListWorkloadBackupOperations(ctx, b.ID)
		if err != nil {
			return
		}
		for _, op := range ops {
			if op.CapturePolicyID != p.ID || op.State != "running" && op.State != "unknown" || op.LeaseUntil.After(currentTime(s.Now)) {
				continue
			}
			claimed, err := s.Records.ClaimWorkloadBackupRecovery(ctx, op.ID, currentTime(s.Now), operationID(s.NewToken))
			if err == nil {
				s.Dispatch.Dispatch(claimed, true)
			}
			return
		}
	}
}

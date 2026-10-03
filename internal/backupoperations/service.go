// Package backupoperations admits captures, claims recovery and executes accepted
// backup operations. HTTP permissions and destructive reviews belong to callers.
package backupoperations

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/backupstore"
	"github.com/doout/dispatch/internal/core"
)

type Records interface {
	GetWorkloadBackup(context.Context, string) (core.WorkloadBackup, error)
	GetServer(context.Context, string) (core.Server, error)
	CompleteWorkloadBackupOperation(context.Context, core.WorkloadBackup, core.WorkloadBackupOperation) error
}

type Authority interface {
	CheckPolicy(context.Context, core.WorkloadBackupOperation) error
	ExecutionGenerationMatches(context.Context, core.WorkloadBackupOperation) bool
}

type RecoveryMaterial interface {
	DecodeOperation(core.WorkloadBackupOperation) (core.WorkloadBackupRequest, error)
	GrantObjects(context.Context, core.WorkloadBackup, string, bool, bool) (backupstore.Access, error)
}

type Execution interface {
	WithTarget(context.Context, string, func() error) error
	Run(context.Context, core.WorkloadBackupRequest, core.Server) (core.WorkloadBackupResult, error)
	DeleteOffsite(context.Context, core.WorkloadBackupRequest) (core.WorkloadBackupResult, error)
}

type Service struct {
	Records   Records
	Authority Authority
	Material  RecoveryMaterial
	Execution Execution
}

// Execute uses the accepted operation's execution window. An operation that
// cannot load before that deadline stays available for a later recovery claim.
// Once loaded, its outcome must be saved even when execution times out.
func (s Service) Execute(parent context.Context, op core.WorkloadBackupOperation, recovering bool) error {
	ctx, cancel := context.WithDeadline(parent, op.LeaseUntil.Add(-time.Minute))
	defer cancel()
	b, err := s.Records.GetWorkloadBackup(ctx, op.BackupID)
	if err != nil {
		return nil
	}
	input, err := s.Material.DecodeOperation(op)
	if err == nil {
		err = s.Authority.CheckPolicy(ctx, op)
	}
	// Preparation has not dispatched a runtime job. Recovery retains uncertainty
	// from the original attempt until inspection supplies a confirmed outcome.
	result := core.WorkloadBackupResult{State: "failed", CleanupState: "complete"}
	if recovering {
		result.State, result.CleanupState = "unknown", "pending"
	}
	if err == nil {
		input, result, err = s.execute(ctx, op, b, input, result, recovering)
	}
	now := time.Now().UTC()
	b, op = applyOutcome(b, op, input, result, err, recovering, ctx.Err() != nil, now)
	return s.Records.CompleteWorkloadBackupOperation(context.WithoutCancel(ctx), b, op)
}

func (s Service) execute(ctx context.Context, op core.WorkloadBackupOperation, b core.WorkloadBackup, input core.WorkloadBackupRequest, result core.WorkloadBackupResult, recovering bool) (core.WorkloadBackupRequest, core.WorkloadBackupResult, error) {
	if op.Action == "delete-offsite" {
		access, err := s.Material.GrantObjects(ctx, b, op.OffsiteStoreID, false, true)
		input.Backup, input.OffsiteAccess = b, &access
		if err != nil {
			return input, result, err
		}
		result, err = s.Execution.DeleteOffsite(ctx, input)
		return input, result, err
	}

	executionServer := b.ServerID
	if op.ExecutionServerID != "" {
		executionServer = op.ExecutionServerID
	}
	server, err := s.Records.GetServer(ctx, executionServer)
	if err != nil || executionServer == b.ServerID && server.AgentNodeID != b.NodeID || op.ExecutionServerID != "" && (server.AgentNodeID != op.ExecutionNodeID || !s.Authority.ExecutionGenerationMatches(ctx, op)) {
		return input, result, errors.New("backup target identity changed")
	}
	input.Backup = b
	if input.OffsiteAccess != nil {
		access, grantErr := s.Material.GrantObjects(ctx, b, op.OffsiteStoreID, op.Action == "export" && !recovering, false)
		input.OffsiteAccess, err = &access, grantErr
	}
	if recovering {
		input.RecoveryAction, input.Action = op.Action, "reconcile"
	}
	if err != nil {
		return input, result, err
	}
	err = s.Execution.WithTarget(ctx, server.ID, func() error {
		if op.ExecutionServerID != "" && !s.Authority.ExecutionGenerationMatches(ctx, op) {
			return errors.New("accepted destination enrollment changed")
		}
		if policyErr := s.Authority.CheckPolicy(ctx, op); policyErr != nil {
			return policyErr
		}
		var err error
		result, err = s.Execution.Run(ctx, input, server)
		return err
	})
	return input, result, err
}

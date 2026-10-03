package backupoperations

import (
	"errors"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
)

// applyOutcome preserves uncertain operations and their recovery material until
// inspection proves what happened to the original archive and cleanup resources.
func applyOutcome(b core.WorkloadBackup, op core.WorkloadBackupOperation, input core.WorkloadBackupRequest, result core.WorkloadBackupResult, err error, recovering, interrupted bool, now time.Time) (core.WorkloadBackup, core.WorkloadBackupOperation) {
	op.State, op.Message, op.CleanupState = "succeeded", result.Message, result.CleanupState
	var runtimeError *runtimecontract.Error
	uncertain := interrupted || result.State == "unknown" || result.CleanupState == "failed" || errors.As(err, &runtimeError) && runtimeError.Code == runtimecontract.Uncertain
	if err != nil || result.State == "failed" {
		op.State = "failed"
		if err != nil || op.Message == "" {
			op.Message = failureMessage(err)
		}
	}
	if uncertain {
		op.State = "unknown"
		if op.Message == "" {
			op.Message = "The outcome is uncertain. Reconcile the original operation after its execution lease ends."
		}
	}
	if result.State == "unresolved" {
		op.State = "unknown"
		if recovering && err == nil {
			op.State = "unresolved"
		}
	}
	switch op.Action {
	case "retire-local":
		if op.State == "succeeded" && result.State == "local-retired" && result.CleanupState == "complete" {
			b.LocalState = "retired"
		}
	case "delete-offsite":
		if op.State == "unknown" && b.Offsite != nil {
			off := *b.Offsite
			off.VerificationState = "deletion-pending"
			b.Offsite = &off
		}
		if op.State == "succeeded" && result.State == "offsite-deleted" && result.CleanupState == "complete" && b.Offsite != nil {
			off := *b.Offsite
			off.DeletedAt = &now
			off.VerificationState = "deleted"
			b.Offsite = &off
			if b.LocalState == "retired" {
				b.State = "deleted"
			}
		}
	case "export":
		if op.State == "succeeded" && result.State == "exported" && result.Offsite != nil {
			b.Offsite = result.Offsite
			b.Offsite.VerificationState, b.Offsite.VerifiedAt = "not_verified", nil
		}
	case "backup":
		b.State = "unknown"
		if op.State == "succeeded" && result.State == "ready" {
			b.State = "ready"
			b.Checksum, b.PlaintextChecksum, b.Bytes, b.ImageID = result.Checksum, result.PlaintextChecksum, result.Bytes, result.ImageID
		}
		if op.State == "failed" {
			b.State = "failed"
		}
	case "restore":
		if b.LocalState == "retired" || op.ExecutionServerID != "" && op.ExecutionServerID != b.ServerID {
			b.CleanupState = result.CleanupState
		}
	case "verify":
		b.VerificationState = "failed"
		b.CleanupState = result.CleanupState
		if op.State == "succeeded" && result.State == "verified" && result.CleanupState == "complete" {
			b.VerificationState = "verified"
			b.VerifiedAt = &now
		}
		if result.CleanupState != "complete" {
			b.VerificationState = "unknown"
			op.State = "unknown"
		}
		if input.OffsiteAccess != nil && b.Offsite != nil {
			b.Offsite.VerificationState = b.VerificationState
			if b.VerificationState == "verified" && op.State == "succeeded" {
				b.Offsite.VerifiedAt = &now
			}
		}
	case "delete":
		if op.State == "succeeded" && result.State == "deleted" {
			b.State = "deleted"
		}
	}
	if b.VerificationIntervalHours > 0 {
		next := now.Add(time.Duration(b.VerificationIntervalHours) * time.Hour)
		b.NextVerificationAt = &next
		if (op.Action == "backup" || op.Action == "export" && b.Offsite != nil) && b.CapturePolicyID != "" && b.State == "ready" {
			b.NextVerificationAt = &now
		}
	}
	b.Message = op.Message
	return b, op
}

func failureMessage(err error) string {
	if err == nil {
		return "Verification failed; inspect its retained operation and cleanup outcome."
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "checksum"):
		return "Backup checksum failed. The archive is corrupt or was replaced."
	case strings.Contains(text, "decrypt") || strings.Contains(text, "authenticat") || strings.Contains(text, "encryption key"):
		return "The archive or recovery manifest cannot be decrypted or authenticated. Check the retained key and original target."
	case strings.Contains(text, "cleanup"):
		return "Isolated verification cleanup failed. Reconcile the original operation before starting another check."
	case strings.Contains(text, "integrity"):
		return "The restored database failed a configured integrity check. Inspect the destination and verification outcome."
	case strings.Contains(text, "ownership") || strings.Contains(text, "identity") || strings.Contains(text, "destination changed"):
		return "Resource ownership or target identity changed. Inspect the original target before another review."
	case strings.Contains(text, "inaccessible") || strings.Contains(text, "unavailable"):
		return "The backup or target storage is inaccessible. Check the original target and retained artifact."
	case strings.Contains(text, "database-native"):
		return "The database-native backup failed. Check database access and target storage space."
	case strings.Contains(text, "restore"):
		return "Data restore failed or was interrupted. Inspect the destination before reviewing another restore."
	default:
		return "Backup execution failed. Check the original target, storage space and recorded cleanup state."
	}
}

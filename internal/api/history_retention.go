package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/backup"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"
)

type automaticHistoryRetention interface {
	ListAutomaticRetentionPolicies(context.Context) ([]core.RetentionPolicy, error)
	ApplyRetentionReviewed(context.Context, core.RetentionPolicy, bool, time.Time) (core.RetentionResult, error)
}

// Cleanup lives in the controller process and uses the same transaction and
// reference checks as manually reviewed cleanup. Multiple controllers serialize
// on the project row. Runtime resources require their own reviewed cleanup.
func (a *API) RunHistoryRetention(ctx context.Context) {
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		a.runHistoryRetention(ctx, time.Now().UTC())
		a.pruneControllerBackups(ctx)
		timer.Reset(time.Hour)
	}
}
func (a *API) runHistoryRetention(ctx context.Context, now time.Time) {
	data, ok := a.store.(automaticHistoryRetention)
	if !ok {
		return
	}
	policies, err := data.ListAutomaticRetentionPolicies(ctx)
	if err != nil {
		a.logger.Error("cannot read history retention policies", "error", err)
		return
	}
	for _, policy := range policies {
		work, cancel := context.WithTimeout(ctx, 2*time.Minute)
		// Limit each project to ten batches per pass so large projects do not starve others.
		for batch := 0; batch < 10; batch++ {
			result, err := data.ApplyRetentionReviewed(work, policy, true, now)
			if err != nil {
				if !errors.Is(err, store.ErrRetentionPolicyChanged) && !errors.Is(err, store.ErrRuntimeRetentionChanged) && !errors.Is(err, context.Canceled) {
					a.logger.Error("history retention failed", "project", policy.ProjectID, "error", err)
				}
				break
			}
			if result.Logs+result.WorkflowLogs+result.Runs+result.Snapshots > 0 {
				a.logger.Info("history retention applied", "project", policy.ProjectID, "logs", result.Logs, "jobLogs", result.WorkflowLogs, "runs", result.Runs, "snapshots", result.Snapshots, "snapshotBytes", result.SnapshotBytes)
				if audit, ok := a.store.(interface {
					AppendAuditEvent(context.Context, core.AuditEvent) error
				}); ok {
					if err := audit.AppendAuditEvent(work, core.AuditEvent{ID: ulid.Make().String(), ActorID: "system", ActorName: "Automatic retention", ActorType: "system", ProjectID: policy.ProjectID, ResourceID: policy.ProjectID, Action: "history.retention", Outcome: "succeeded", CreatedAt: now}); err != nil {
						a.logger.Error("cannot record automatic retention", "project", policy.ProjectID, "error", err)
					}
				}
			}
			if !result.More {
				break
			}
		}
		cancel()
	}
}
func (a *API) setDeploymentRetentionPin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Pinned *bool `json:"pinned"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Pinned == nil {
		problem(w, 400, "Pin required", "Supply pinned as true or false.")
		return
	}
	data, ok := a.store.(interface {
		SetDeploymentRetentionPin(context.Context, string, bool) error
	})
	if !ok {
		problem(w, 503, "Retention unavailable", "Release retention is not configured.")
		return
	}
	if err := data.SetDeploymentRetentionPin(r.Context(), chi.URLParam(r, "id"), *input.Pinned); err != nil {
		if errors.Is(err, store.ErrDeploymentInputsExpired) || errors.Is(err, store.ErrRuntimeRetentionChanged) {
			problem(w, 409, "Inputs unavailable", err.Error())
			return
		}
		a.internal(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"pinned": *input.Pinned})
}

func (a *API) pruneControllerBackups(ctx context.Context) {
	if a.eventConfig.BackupDirectory == "" || a.eventConfig.MasterKeyFile == "" || !a.backupMu.TryLock() {
		return
	}
	defer a.backupMu.Unlock()
	data, ok := a.store.(backup.Repository)
	if !ok {
		return
	}
	manager := backup.Manager{Store: data, Directory: a.eventConfig.BackupDirectory, MasterKeyFile: a.eventConfig.MasterKeyFile, DatabaseURL: a.eventConfig.DatabaseURL}
	if removed, err := manager.PruneVerified(ctx, backup.DefaultVerifiedCopies); err != nil {
		a.logger.Warn("controller backup retention skipped", "error", err)
	} else if removed > 0 {
		a.logger.Info("controller backups pruned", "count", removed)
	}
}

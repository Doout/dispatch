package store

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// Only the accepted deployment and the environment's owned cleanup may mutate
// its application. Interactive inspect/logs remain available for recovery.
const temporaryRuntimeGuard = ` NOT EXISTS (SELECT 1 FROM temporary_environments e WHERE e.app_id=runtime_jobs.app_id AND NOT (
 runtime_jobs.operation IN ('inspect','logs','retention_inspect') OR (runtime_jobs.operation='retention_prune' AND e.state='closed') OR
 (runtime_jobs.operation='deploy' AND runtime_jobs.id='deploy-' || e.deployment_id AND e.state NOT IN ('closing','cleanup_blocked','closed') AND e.expires_at>?) OR
 (runtime_jobs.operation='destroy' AND runtime_jobs.id=e.cleanup_job_id AND e.state IN ('closing','cleanup_blocked')))) `

func (s *SQLStore) checkTemporaryRuntime(ctx context.Context, tx *changeTx, j *core.RuntimeJob) error {
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE temporary_environments SET revision=revision WHERE app_id=?`), j.AppID); err != nil {
		return err
	}
	e, err := scanTemporary(tx.QueryRowContext(ctx, s.q(`SELECT `+temporaryColumns+` FROM temporary_environments WHERE app_id=?`), j.AppID))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if j.Operation == "inspect" || j.Operation == "logs" || j.Operation == "retention_inspect" || j.Operation == "retention_prune" && e.State == "closed" {
		return nil
	}
	if j.NodeID != e.TargetNodeID || j.NodeGeneration != e.TargetGeneration {
		return ErrTemporaryEnvironmentChanged
	}
	if j.Operation == "destroy" && j.ID == e.CleanupJobID && (e.State == "closing" || e.State == "cleanup_blocked") {
		return nil
	}
	if j.Operation != "deploy" || j.ID != "deploy-"+e.DeploymentID || e.State == "closing" || e.State == "cleanup_blocked" || e.State == "closed" || !e.ExpiresAt.After(time.Now().UTC()) {
		return ErrTemporaryEnvironmentChanged
	}
	if e.ExpiresAt.Before(j.ExpiresAt) {
		j.ExpiresAt = e.ExpiresAt
	}
	return nil
}

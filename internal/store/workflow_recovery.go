package store

import (
	"context"
	"time"
)

// RecoverInterruptedWorkflows is a startup-only reconciliation for the single
// controller. It records uncertainty rather than replaying jobs or deployments.
// Awaiting approvals and terminal successful artifacts remain unchanged.
func (s *SQLStore) RecoverInterruptedWorkflows(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	message := "Controller restarted while this work was in progress. Inspect the existing deployment and external side effects, then explicitly retry the workflow. No work was replayed."
	var affected int64
	for _, table := range []string{"workflow_job_results", "workflow_stage_runs", "workflow_revisions"} {
		result, err := tx.ExecContext(ctx, s.q(`UPDATE `+table+` SET state='failed',error=?,finished_at=? WHERE state IN ('queued','running') AND created_at<?`), message, stamp(before), stamp(before))
		if err != nil {
			return 0, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		affected += count
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return affected, nil
}

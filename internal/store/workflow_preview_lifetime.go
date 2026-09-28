package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) WorkflowPreviewLifetimeCommentHandled(ctx context.Context, triggerID, commentID string) (bool, error) {
	var handled sql.NullString
	err := s.db.QueryRowContext(ctx, s.q(`SELECT lifetime_handled_at FROM workflow_preview_comments WHERE trigger_id=? AND comment_id=?`), triggerID, commentID).Scan(&handled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return handled.Valid, err
}

func (s *SQLStore) MarkWorkflowPreviewLifetimeReported(ctx context.Context, trigger core.WorkflowPreviewTrigger, resourceState string) error {
	_, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET lifetime_report_pending=FALSE WHERE id=? AND COALESCE(expires_at,'')=COALESCE(?,'')
		AND EXISTS (SELECT 1 FROM workflow_resources r WHERE r.id=workflow_preview_triggers.resource_id AND r.state=?)`), trigger.ID, nullTime(trigger.ExpiresAt), resourceState)
	return err
}

// A receipt and deadline change commit together. Poll overlap, webhook delivery,
// and a restart therefore cannot extend the same preview twice.
func (s *SQLStore) SaveWorkflowPreviewLifetime(ctx context.Context, previous, next core.WorkflowPreviewTrigger, commentID string, now time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_comments(trigger_id,comment_id,lifetime_handled_at) VALUES(?,?,?) ON CONFLICT(trigger_id,comment_id) DO NOTHING`), next.ID, commentID, stamp(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET ttl=?,expires_at=?,lifetime_report_pending=TRUE WHERE id=? AND closed_at IS NULL
		AND ttl=? AND COALESCE(expires_at,'')=COALESCE(?,'')
		AND EXISTS (SELECT 1 FROM workflow_resources r WHERE r.id=workflow_preview_triggers.resource_id AND r.temporary=TRUE AND r.active=TRUE AND r.state NOT IN ('expiring','expired','removed'))`),
		next.TTL, nullTime(next.ExpiresAt), next.ID, previous.TTL, nullTime(previous.ExpiresAt))
	if err := changed(result, err); err != nil {
		return false, fmt.Errorf("preview lifetime changed or cleanup has started; check the preview and retry with a new comment: %w", err)
	}
	return true, tx.Commit()
}

// Claim against the current deadline, rather than the poller's snapshot. A
// concurrent extension wins if it commits before cleanup starts. The lease
// lets a restarted controller resume interrupted cleanup.
func (s *SQLStore) ClaimWorkflowPreviewExpiry(ctx context.Context, triggerID string, now, leaseUntil time.Time) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET cleanup_lease_until=? WHERE id=? AND closed_at IS NULL
		AND expires_at IS NOT NULL AND expires_at<=? AND (cleanup_lease_until IS NULL OR cleanup_lease_until<=?)
		AND EXISTS (SELECT 1 FROM workflow_resources r WHERE r.id=workflow_preview_triggers.resource_id AND r.temporary=TRUE AND r.state NOT IN ('removed','expired'))
		AND NOT EXISTS (SELECT 1 FROM workflow_preview_triggers other WHERE other.resource_id=workflow_preview_triggers.resource_id AND other.id<>workflow_preview_triggers.id AND other.cleanup_lease_until>?)`), stamp(leaseUntil), triggerID, stamp(now), stamp(now), stamp(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET active=FALSE,state='expiring',last_error='',updated_at=? WHERE id=(SELECT resource_id FROM workflow_preview_triggers WHERE id=?)`), stamp(now), triggerID)
	if err := changed(result, err); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *SQLStore) FinishWorkflowPreviewExpiry(ctx context.Context, triggerID string, leaseUntil time.Time, detail string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	state := "expired"
	if detail != "" {
		state = "expiring"
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET active=FALSE,state=?,last_error=?,updated_at=? WHERE id=(SELECT resource_id FROM workflow_preview_triggers WHERE id=? AND cleanup_lease_until=?)`), state, detail, stamp(now), triggerID, stamp(leaseUntil))
	if err := changed(result, err); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET cleanup_lease_until=NULL,lifetime_report_pending=TRUE WHERE id=? AND cleanup_lease_until=?`), triggerID, stamp(leaseUntil))
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

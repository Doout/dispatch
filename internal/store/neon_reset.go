package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"time"
)

// Replacement is accepted only while the preview is paused and no revision can
// resolve its alias. The old binding and branch survive every failure path.
func (s *SQLStore) checkNeonReplacement(ctx context.Context, tx *changeTx, r core.ServiceResource, accepting bool) error {
	if r.Target.Provider != "neon" || r.PreviewID == "" || r.PreviewAlias == "" || r.ReplacesRunID == r.RunID {
		return ErrServiceResourceChanged
	}
	if err := s.lockPreviewResource(ctx, tx, r.PreviewID, true); err != nil {
		return err
	}
	var project, state string
	var active, temporary bool
	if err := tx.QueryRowContext(ctx, s.q(`SELECT c.project_id,w.active,w.temporary,w.state FROM workflow_resources w JOIN config_sources c ON c.id=w.config_source_id WHERE w.id=?`), r.PreviewID).Scan(&project, &active, &temporary, &state); err != nil {
		return err
	}
	if project != r.ProjectID || active || !temporary || state == "expiring" || state == "expired" || state == "removed" {
		return ErrPreviewClosing
	}
	var n int
	for _, q := range []string{`SELECT COUNT(*) FROM workflow_preview_cleanups WHERE resource_id=? AND state<>'succeeded'`, `SELECT COUNT(*) FROM workflow_revisions WHERE resource_id=? AND state IN ('queued','running','awaiting_approval')`} {
		if err := tx.QueryRowContext(ctx, s.q(q), r.PreviewID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrServiceInUse
		}
	}
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workflow_preview_triggers WHERE resource_id=? AND (closed_at IS NOT NULL OR (expires_at IS NOT NULL AND expires_at<=?))`), r.PreviewID, stamp(time.Now().UTC())).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrPreviewClosing
	}
	var linked string
	if err := tx.QueryRowContext(ctx, s.q(`SELECT run_id FROM neon_preview_services WHERE preview_id=? AND alias=?`), r.PreviewID, r.PreviewAlias).Scan(&linked); err != nil {
		return err
	}
	if linked != r.ReplacesRunID {
		return ErrServiceResourceChanged
	}
	q := `SELECT ` + serviceResourceColumns + ` FROM service_resources WHERE run_id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	source, err := scanServiceResource(tx.QueryRowContext(ctx, s.q(q), r.ReplacesRunID))
	if err != nil {
		return err
	}
	if source.Target.Provider != "neon" || source.ProjectID != r.ProjectID || source.PreviewID != r.PreviewID || source.PreviewAlias != r.PreviewAlias || source.State != "ready" || source.LeaseUntil.After(time.Now()) {
		return ErrServiceResourceChanged
	}
	if accepting {
		if source.Revision != r.ReplacesRevision {
			return ErrServiceResourceChanged
		}
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM neon_replacements WHERE preview_id=? AND state IN ('accepted','unresolved')`), r.PreviewID).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return ErrServiceResourceChanged
		}
	} else {
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM neon_replacements WHERE replacement_run_id=? AND source_run_id=? AND state IN ('accepted','unresolved')`), r.RunID, r.ReplacesRunID).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return ErrServiceResourceChanged
		}
	}
	return nil
}

func (s *SQLStore) finishNeonReplacement(ctx context.Context, tx *changeTx, r core.ServiceResource, item *core.Service) error {
	var state string
	err := tx.QueryRowContext(ctx, s.q(`SELECT state FROM neon_replacements WHERE replacement_run_id=? AND source_run_id=? AND preview_id=? AND alias=?`), r.RunID, r.ReplacesRunID, r.PreviewID, r.PreviewAlias).Scan(&state)
	if err != nil {
		return err
	}
	if state == "succeeded" || state == "cancelled" {
		return nil
	}
	next := "unresolved"
	if r.State == "ready" {
		if item == nil || r.EncryptedOutputs == "" || r.ResourceID == "" {
			return ErrServiceResourceChanged
		}
		if err = s.checkNeonReplacement(ctx, tx, r, false); err != nil {
			return err
		}
		result, e := tx.ExecContext(ctx, s.q(`UPDATE neon_preview_services SET run_id=? WHERE preview_id=? AND alias=? AND run_id=?`), r.RunID, r.PreviewID, r.PreviewAlias, r.ReplacesRunID)
		if err = changed(result, e); err != nil {
			return err
		}
		// The old generation remains visible and cannot inherit automatic deletion.
		source, e := scanServiceResource(tx.QueryRowContext(ctx, s.q(`SELECT `+serviceResourceColumns+` FROM service_resources WHERE run_id=?`), r.ReplacesRunID))
		if e != nil {
			return e
		}
		before := source.Revision
		source.ReplacedByRunID = r.RunID
		source.Policy, source.Message = "retain", "Retained after schema-only replacement; delete separately after reviewing all consumers."
		source.Revision++
		source.UpdatedAt = time.Now().UTC()
		result, e = tx.ExecContext(ctx, s.q(`UPDATE service_resources SET revision=?,payload=? WHERE run_id=? AND revision=?`), source.Revision, jsonText(source), source.RunID, before)
		if err = changed(result, e); err != nil {
			return err
		}
		next = "succeeded"
	} else if r.State == "deleted" {
		next = "cancelled"
	}
	_, err = tx.ExecContext(ctx, s.q(`UPDATE neon_replacements SET state=? WHERE replacement_run_id=?`), next, r.RunID)
	return err
}

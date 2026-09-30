package store

import (
	"context"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) ListWorkflowPreviewPanels(ctx context.Context) ([]core.WorkflowPreviewPanel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,template_id,github_app_id,repository,pull_request_number,resource_id,comment_id,body,action_error FROM workflow_preview_panels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowPreviewPanel{}
	for rows.Next() {
		var p core.WorkflowPreviewPanel
		if err := rows.Scan(&p.ID, &p.TemplateID, &p.GitHubAppID, &p.Repository, &p.PullRequestNumber, &p.ResourceID, &p.CommentID, &p.Body, &p.ActionError); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func (s *SQLStore) SaveWorkflowPreviewPanel(ctx context.Context, p core.WorkflowPreviewPanel) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_panels(id,template_id,github_app_id,repository,pull_request_number,resource_id,comment_id,body,action_error) VALUES(?,?,?,?,?,?,?,?,?)
 ON CONFLICT(github_app_id,repository,pull_request_number,template_id) DO UPDATE SET resource_id=excluded.resource_id,comment_id=excluded.comment_id,body=excluded.body,action_error=excluded.action_error`), p.ID, p.TemplateID, p.GitHubAppID, p.Repository, p.PullRequestNumber, p.ResourceID, p.CommentID, p.Body, p.ActionError)
	return err
}

// Removal is committed only after runtime cleanup succeeds. Keep run history.
func (s *SQLStore) RemoveWorkflowPreviewResource(ctx context.Context, id string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET active=FALSE,state='removed',last_error='',updated_at=? WHERE id=? AND temporary=TRUE`), stamp(now), id)
	if err := changed(result, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET closed_at=COALESCE(closed_at,?),lifetime_report_pending=TRUE WHERE resource_id=?`), stamp(now), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) SetWorkflowPreviewLiveReload(ctx context.Context, id string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET live_reload=?,lifetime_report_pending=TRUE WHERE id=? AND closed_at IS NULL`), enabled, id)
	return changed(result, err)
}

func (s *SQLStore) ClaimWorkflowPreviewPanelLease(ctx context.Context, holder string, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_panel_leases(id,holder,lease_until) VALUES('panels',?,?)
 ON CONFLICT(id) DO UPDATE SET holder=excluded.holder,lease_until=excluded.lease_until WHERE workflow_preview_panel_leases.lease_until<=?`), holder, stamp(now.Add(10*time.Minute)), stamp(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}
func (s *SQLStore) ReleaseWorkflowPreviewPanelLease(ctx context.Context, holder string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM workflow_preview_panel_leases WHERE id='panels' AND holder=?`), holder)
	return err
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) CreateWorkflowPreviewTrigger(ctx context.Context, item core.WorkflowPreviewTrigger) error {
	if item.TTL == "" {
		item.TTL = "0"
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_triggers(id,resource_id,github_app_id,repository,pull_request_number,command,preview_url,linked_pull_requests,template_id,created_at,template_source,auto_deploy,max_auto_runs_per_hour,source_defaults,ttl,expires_at,cleanup_lease_until,live_reload) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.ResourceID, item.GitHubAppID, item.Repository, item.PullRequestNumber, item.Command, item.PreviewURL, jsonText(item.LinkedPullRequests), nullString(item.TemplateID), stamp(item.CreatedAt), jsonText(item.TemplateSource), item.AutoDeploy, item.MaxAutoRunsPerHour, jsonText(item.SourceDefaults), item.TTL, nullTime(item.ExpiresAt), nullTime(item.CleanupLeaseUntil), item.LiveReload)
	return err
}

func (s *SQLStore) ListWorkflowPreviewTriggers(ctx context.Context) ([]core.WorkflowPreviewTrigger, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,resource_id,github_app_id,repository,pull_request_number,command,preview_url,report_comment_id,linked_pull_requests,template_id,created_at,closed_at,template_source,auto_deploy,max_auto_runs_per_hour,source_defaults,ttl,expires_at,cleanup_lease_until,lifetime_report_pending,lifetime_start_comment_id,live_reload,live_reload_comment_id FROM workflow_preview_triggers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowPreviewTrigger{}
	for rows.Next() {
		var item core.WorkflowPreviewTrigger
		var created string
		var closed, expires, lease sql.NullString
		var templateID sql.NullString
		var linked, templateSource, defaults string
		if err := rows.Scan(&item.ID, &item.ResourceID, &item.GitHubAppID, &item.Repository, &item.PullRequestNumber, &item.Command, &item.PreviewURL, &item.ReportCommentID, &linked, &templateID, &created, &closed, &templateSource, &item.AutoDeploy, &item.MaxAutoRunsPerHour, &defaults, &item.TTL, &expires, &lease, &item.LifetimeReportPending, &item.LifetimeStartCommentID, &item.LiveReload, &item.LiveReloadCommentID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(templateSource), &item.TemplateSource); err != nil {
			return nil, err
		}
		item.TemplateID = templateID.String
		if err := json.Unmarshal([]byte(linked), &item.LinkedPullRequests); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(defaults), &item.SourceDefaults); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.ClosedAt = parseNullTime(closed)
		item.ExpiresAt, item.CleanupLeaseUntil = parseNullTime(expires), parseNullTime(lease)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) UpdateWorkflowPreviewTrigger(ctx context.Context, item core.WorkflowPreviewTrigger) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET github_app_id=?,repository=?,pull_request_number=?,command=?,preview_url=?,auto_deploy=?,max_auto_runs_per_hour=?,live_reload=?,ttl=?,expires_at=?,lifetime_report_pending=TRUE,
		report_comment_id=CASE WHEN github_app_id=? AND repository=? AND pull_request_number=? THEN report_comment_id ELSE '' END,
		linked_pull_requests=CASE WHEN github_app_id=? AND repository=? AND pull_request_number=? THEN linked_pull_requests ELSE '{}' END,
		live_reload_comment_id=CASE WHEN github_app_id=? AND repository=? AND pull_request_number=? AND command=? THEN live_reload_comment_id ELSE '' END
		WHERE id=? AND closed_at IS NULL AND EXISTS (SELECT 1 FROM workflow_resources r WHERE r.id=workflow_preview_triggers.resource_id AND r.active=TRUE AND r.state NOT IN ('expiring','expired','removed'))`), item.GitHubAppID, item.Repository, item.PullRequestNumber, item.Command, item.PreviewURL, item.AutoDeploy, item.MaxAutoRunsPerHour, item.LiveReload, item.TTL, nullTime(item.ExpiresAt),
		item.GitHubAppID, item.Repository, item.PullRequestNumber, item.GitHubAppID, item.Repository, item.PullRequestNumber, item.GitHubAppID, item.Repository, item.PullRequestNumber, item.Command, item.ID)
	return changed(result, err)
}

// Newer GitHub comments win even if webhook delivery and polling arrive out of order.
func (s *SQLStore) UpdateWorkflowPreviewLiveReload(ctx context.Context, id, commentID string, enabled bool) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET live_reload=?,live_reload_comment_id=?,lifetime_report_pending=TRUE
		WHERE id=? AND closed_at IS NULL AND EXISTS (SELECT 1 FROM workflow_resources r WHERE r.id=workflow_preview_triggers.resource_id AND r.active=TRUE AND r.state NOT IN ('expiring','expired','removed'))
		AND (live_reload_comment_id='' OR length(live_reload_comment_id)<length(?) OR (length(live_reload_comment_id)=length(?) AND live_reload_comment_id<?))`), enabled, commentID, id, commentID, commentID, commentID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *SQLStore) UpdateWorkflowPreviewTriggerLinks(ctx context.Context, id string, links map[string]int) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET linked_pull_requests=? WHERE id=? AND closed_at IS NULL`), jsonText(links), id)
	return changed(result, err)
}

// SaveWorkflowPreviewSources updates the pinned definition and its link state
// together, so a retry cannot lose the source's original reference.
func (s *SQLStore) SaveWorkflowPreviewSources(ctx context.Context, resource core.WorkflowResource, trigger core.WorkflowPreviewTrigger) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET linked_pull_requests=?,source_defaults=?,ttl=?,expires_at=?,lifetime_start_comment_id=? WHERE id=? AND resource_id=? AND closed_at IS NULL`), jsonText(trigger.LinkedPullRequests), jsonText(trigger.SourceDefaults), trigger.TTL, nullTime(trigger.ExpiresAt), trigger.LifetimeStartCommentID, trigger.ID, resource.ID)
	if err := changed(result, err); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, s.q(`UPDATE workflow_resources SET document=?,spec_digest=?,updated_at=?,active=TRUE,state='ready',last_error='' WHERE id=? AND temporary=TRUE AND ((active=TRUE AND state NOT IN ('expiring','removed')) OR state IN ('expired','paused'))`), resource.Document, resource.SpecDigest, stamp(resource.UpdatedAt), resource.ID)
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) LatestWorkflowPreviewDeployComment(ctx context.Context, triggerID string) (string, error) {
	var id string
	// GitHub comment IDs are positive decimal numbers. Length then lexical
	// order compares them without database-specific integer casts.
	err := s.db.QueryRowContext(ctx, s.q(`SELECT c.comment_id FROM workflow_preview_comments c LEFT JOIN workflow_revisions r ON r.id=c.revision_id
		WHERE c.trigger_id=? AND (r.trigger_name LIKE 'pull request comment %' OR c.lifetime_handled_at IS NOT NULL)
		ORDER BY length(c.comment_id) DESC,c.comment_id DESC LIMIT 1`), triggerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *SQLStore) UpdateWorkflowPreviewTriggerURL(ctx context.Context, id, previewURL string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET preview_url=? WHERE id=? AND closed_at IS NULL`), previewURL, id)
	return changed(result, err)
}

func (s *SQLStore) UpdateWorkflowPreviewTriggerComment(ctx context.Context, id, commentID string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET report_comment_id=? WHERE id=? AND closed_at IS NULL`), commentID, id)
	return changed(result, err)
}

func (s *SQLStore) PendingWorkflowPreviewReports(ctx context.Context, triggerID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT r.id FROM workflow_revisions r
		JOIN workflow_preview_triggers t ON t.resource_id=r.resource_id
		WHERE t.id=? AND r.state IN ('succeeded','failed','cancelled') AND
		(r.trigger_name='pull request update' OR EXISTS (
			SELECT 1 FROM workflow_preview_comments c WHERE c.trigger_id=t.id AND c.revision_id=r.id))
		AND NOT EXISTS (SELECT 1 FROM workflow_preview_reports p WHERE p.revision_id=r.id
			AND p.repository=t.repository AND p.pull_request_number=t.pull_request_number)
		ORDER BY r.created_at`), triggerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	return items, rows.Err()
}

func (s *SQLStore) CloseWorkflowPreviewTrigger(ctx context.Context, id string, closedAt time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET closed_at=? WHERE id=? AND closed_at IS NULL`), stamp(closedAt), id)
	return changed(result, err)
}

func (s *SQLStore) ReserveWorkflowPreviewComment(ctx context.Context, triggerID, commentID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_comments(trigger_id,comment_id) VALUES(?,?) ON CONFLICT(trigger_id,comment_id) DO NOTHING`), triggerID, commentID)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func (s *SQLStore) CompleteWorkflowPreviewComment(ctx context.Context, triggerID, commentID, revisionID string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_comments SET revision_id=? WHERE trigger_id=? AND comment_id=?`), revisionID, triggerID, commentID)
	return changed(result, err)
}

func (s *SQLStore) UpdateWorkflowPreviewTestComment(ctx context.Context, triggerID, sourceCommentID, statusCommentID string) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_comments SET status_comment_id=? WHERE trigger_id=? AND comment_id=?`), statusCommentID, triggerID, sourceCommentID)
	return changed(result, err)
}

func (s *SQLStore) WorkflowPreviewTestComment(ctx context.Context, revisionID string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT status_comment_id FROM workflow_preview_comments WHERE revision_id=?`), revisionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (s *SQLStore) ReleaseWorkflowPreviewComment(ctx context.Context, triggerID, commentID string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM workflow_preview_comments WHERE trigger_id=? AND comment_id=? AND revision_id IS NULL AND lifetime_handled_at IS NULL`), triggerID, commentID)
	return err
}

func (s *SQLStore) WorkflowPreviewCommentRevision(ctx context.Context, triggerID, commentID string) (string, error) {
	var id sql.NullString
	err := s.db.QueryRowContext(ctx, s.q("SELECT revision_id FROM workflow_preview_comments WHERE trigger_id=? AND comment_id=?"), triggerID, commentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id.String, err
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) CreateWorkflowPreviewTrigger(ctx context.Context, item core.WorkflowPreviewTrigger) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_triggers(id,resource_id,github_app_id,repository,pull_request_number,command,preview_url,linked_pull_requests,template_id,created_at,template_source) VALUES(?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.ResourceID, item.GitHubAppID, item.Repository, item.PullRequestNumber, item.Command, item.PreviewURL, jsonText(item.LinkedPullRequests), nullString(item.TemplateID), stamp(item.CreatedAt), jsonText(item.TemplateSource))
	return err
}

func (s *SQLStore) ListWorkflowPreviewTriggers(ctx context.Context) ([]core.WorkflowPreviewTrigger, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,resource_id,github_app_id,repository,pull_request_number,command,preview_url,report_comment_id,linked_pull_requests,template_id,created_at,closed_at,template_source FROM workflow_preview_triggers ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowPreviewTrigger{}
	for rows.Next() {
		var item core.WorkflowPreviewTrigger
		var created string
		var closed sql.NullString
		var templateID sql.NullString
		var linked, templateSource string
		if err := rows.Scan(&item.ID, &item.ResourceID, &item.GitHubAppID, &item.Repository, &item.PullRequestNumber, &item.Command, &item.PreviewURL, &item.ReportCommentID, &linked, &templateID, &created, &closed, &templateSource); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(templateSource), &item.TemplateSource); err != nil {
			return nil, err
		}
		item.TemplateID = templateID.String
		if err := json.Unmarshal([]byte(linked), &item.LinkedPullRequests); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		item.ClosedAt = parseNullTime(closed)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) UpdateWorkflowPreviewTrigger(ctx context.Context, item core.WorkflowPreviewTrigger) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET github_app_id=?,repository=?,pull_request_number=?,command=?,preview_url=?,
		report_comment_id=CASE WHEN github_app_id=? AND repository=? AND pull_request_number=? THEN report_comment_id ELSE '' END,
		linked_pull_requests=CASE WHEN github_app_id=? AND repository=? AND pull_request_number=? THEN linked_pull_requests ELSE '{}' END
		WHERE id=? AND closed_at IS NULL`), item.GitHubAppID, item.Repository, item.PullRequestNumber, item.Command, item.PreviewURL,
		item.GitHubAppID, item.Repository, item.PullRequestNumber, item.GitHubAppID, item.Repository, item.PullRequestNumber, item.ID)
	return changed(result, err)
}

func (s *SQLStore) UpdateWorkflowPreviewTriggerLinks(ctx context.Context, id string, links map[string]int) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET linked_pull_requests=? WHERE id=? AND closed_at IS NULL`), jsonText(links), id)
	return changed(result, err)
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
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT DISTINCT c.revision_id FROM workflow_preview_comments c
		JOIN workflow_revisions r ON r.id=c.revision_id
		JOIN workflow_preview_triggers t ON t.id=c.trigger_id
		WHERE c.trigger_id=? AND r.state='succeeded' AND NOT EXISTS (
			SELECT 1 FROM workflow_preview_reports p WHERE p.revision_id=c.revision_id
			AND p.repository=t.repository AND p.pull_request_number=t.pull_request_number)
		ORDER BY c.revision_id`), triggerID)
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

func (s *SQLStore) ReleaseWorkflowPreviewComment(ctx context.Context, triggerID, commentID string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM workflow_preview_comments WHERE trigger_id=? AND comment_id=? AND revision_id IS NULL`), triggerID, commentID)
	return err
}

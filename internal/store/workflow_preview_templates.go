package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

const workflowPreviewTemplateSelect = `SELECT id,config_source_id,github_app_id,name,repository,command,preview_url,document,active,created_at,updated_at,git_source,watch_repositories,auto_deploy,max_auto_runs_per_hour,ttl,live_reload,comment_on_open,source_trust_policy FROM workflow_preview_templates`

func (s *SQLStore) CreateWorkflowPreviewTemplate(ctx context.Context, item core.WorkflowPreviewTemplate) error {
	if item.TTL == "" {
		item.TTL = "0"
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_templates(id,config_source_id,github_app_id,name,repository,command,preview_url,document,active,created_at,updated_at,git_source,watch_repositories,auto_deploy,max_auto_runs_per_hour,ttl,live_reload,comment_on_open,source_trust_policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.ConfigSourceID, item.GitHubAppID, item.Name, item.Repository, item.Command, item.PreviewURL, item.Document, item.Active, stamp(item.CreatedAt), stamp(item.UpdatedAt), jsonText(item.GitSource), jsonText(item.WatchRepositories), item.AutoDeploy, item.MaxAutoRunsPerHour, item.TTL, item.LiveReload, item.CommentOnOpen, item.SourceTrustPolicy)
	return err
}

func (s *SQLStore) UpdateWorkflowPreviewTemplate(ctx context.Context, item core.WorkflowPreviewTemplate) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE workflow_preview_templates SET config_source_id=?,github_app_id=?,name=?,repository=?,command=?,preview_url=?,document=?,active=?,updated_at=?,git_source=?,watch_repositories=?,auto_deploy=?,max_auto_runs_per_hour=?,ttl=?,live_reload=?,comment_on_open=?,source_trust_policy=? WHERE id=?`),
		item.ConfigSourceID, item.GitHubAppID, item.Name, item.Repository, item.Command, item.PreviewURL, item.Document, item.Active, stamp(item.UpdatedAt), jsonText(item.GitSource), jsonText(item.WatchRepositories), item.AutoDeploy, item.MaxAutoRunsPerHour, item.TTL, item.LiveReload, item.CommentOnOpen, item.SourceTrustPolicy, item.ID)
	return changed(result, err)
}

func (s *SQLStore) DeleteWorkflowPreviewTemplate(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE workflow_preview_triggers SET source_trust_policy=(SELECT source_trust_policy FROM workflow_preview_templates WHERE id=?) WHERE template_id=?`), id, id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`DELETE FROM workflow_preview_templates WHERE id=?`), id)
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) GetWorkflowPreviewTemplate(ctx context.Context, id string) (core.WorkflowPreviewTemplate, error) {
	item, err := scanWorkflowPreviewTemplate(s.db.QueryRowContext(ctx, s.q(workflowPreviewTemplateSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListWorkflowPreviewTemplates(ctx context.Context) ([]core.WorkflowPreviewTemplate, error) {
	rows, err := s.db.QueryContext(ctx, workflowPreviewTemplateSelect+` ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.WorkflowPreviewTemplate{}
	for rows.Next() {
		item, err := scanWorkflowPreviewTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanWorkflowPreviewTemplate(row scanner) (core.WorkflowPreviewTemplate, error) {
	var item core.WorkflowPreviewTemplate
	var created, updated, gitSource, watchRepositories string
	err := row.Scan(&item.ID, &item.ConfigSourceID, &item.GitHubAppID, &item.Name, &item.Repository, &item.Command, &item.PreviewURL, &item.Document, &item.Active, &created, &updated, &gitSource, &watchRepositories, &item.AutoDeploy, &item.MaxAutoRunsPerHour, &item.TTL, &item.LiveReload, &item.CommentOnOpen, &item.SourceTrustPolicy)
	if err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(gitSource), &item.GitSource); err != nil {
		return item, err
	}
	if err := json.Unmarshal([]byte(watchRepositories), &item.WatchRepositories); err != nil {
		return item, err
	}
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, err
}

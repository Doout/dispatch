package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *SQLStore) WorkflowPreviewReportComment(ctx context.Context, revisionID, repository string, number int) (string, error) {
	var commentID string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT comment_id FROM workflow_preview_reports WHERE revision_id=? AND repository=? AND pull_request_number=?`), revisionID, repository, number).Scan(&commentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return commentID, err
}

func (s *SQLStore) SaveWorkflowPreviewReportComment(ctx context.Context, revisionID, repository string, number int, commentID string) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workflow_preview_reports(revision_id,repository,pull_request_number,comment_id,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(revision_id,repository,pull_request_number) DO UPDATE SET comment_id=excluded.comment_id,updated_at=excluded.updated_at`),
		revisionID, repository, number, commentID, stamp(time.Now().UTC()))
	return err
}

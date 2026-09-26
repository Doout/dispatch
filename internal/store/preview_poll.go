package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *SQLStore) PreviewPollCursor(ctx context.Context, connectionID, repository string) (*time.Time, error) {
	var value string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT checked_at FROM preview_poll_cursors WHERE github_app_id=? AND repository=?`), connectionID, repository).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parsed := parseTime(value)
	return &parsed, nil
}

func (s *SQLStore) SavePreviewPollCursor(ctx context.Context, connectionID, repository string, checkedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO preview_poll_cursors(github_app_id,repository,checked_at) VALUES(?,?,?)
		ON CONFLICT(github_app_id,repository) DO UPDATE SET checked_at=excluded.checked_at`), connectionID, repository, stamp(checkedAt))
	return err
}

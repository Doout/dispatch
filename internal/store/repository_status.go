package store

import (
	"context"
	"errors"
	"strings"
	"time"
)

// RecordDeletedRepository is called only after a per-App webhook signature and
// durable delivery have been verified. It changes evidence, never configuration.
func (s *SQLStore) RecordDeletedRepository(ctx context.Context, appID string, repositoryID int64, fullName, deliveryID string, observedAt time.Time) error {
	if appID == "" || repositoryID < 1 || fullName == "" || deliveryID == "" {
		return errors.New("repository deletion requires verified identity and delivery")
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO repository_deletions(github_app_id,repository_id,full_name,delivery_id,observed_at) VALUES(?,?,?,?,?) ON CONFLICT(github_app_id,repository_id) DO NOTHING`), appID, repositoryID, strings.ToLower(fullName), deliveryID, stamp(observedAt))
	return err
}
func (s *SQLStore) RepositoryDeleted(ctx context.Context, appID string, repositoryID int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM repository_deletions WHERE github_app_id=? AND repository_id=?`), appID, repositoryID).Scan(&n)
	return n > 0, err
}

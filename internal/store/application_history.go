package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
)

// ListApplicationHistory reads a bounded page without hydrating current app settings
// or loading potentially large historical value snapshots.
func (s *SQLStore) ListApplicationHistory(ctx context.Context, appID, beforeID string, limit int) ([]core.Deployment, error) {
	if limit < 1 || limit > 101 {
		limit = 51
	}
	query := `SELECT id,app_id,commit_sha,spec_digest,state,'',created_at,started_at,finished_at,lease_until,'{}','{}' FROM deployments WHERE app_id=?`
	args := []any{appID}
	if beforeID != "" {
		query += ` AND (created_at < (SELECT created_at FROM deployments WHERE id=? AND app_id=?) OR (created_at=(SELECT created_at FROM deployments WHERE id=? AND app_id=?) AND id<?))`
		args = append(args, beforeID, appID, beforeID, appID, beforeID)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Deployment{}
	for rows.Next() {
		item, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

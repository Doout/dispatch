package store

import (
	"context"
	"strings"

	"github.com/doout/dispatch/internal/core"
)

// SearchDeploymentHistory applies authorization before pagination and omits
// credentials, outputs, and potentially large snapshots from the result.
func (s *SQLStore) SearchDeploymentHistory(ctx context.Context, filter core.DeploymentSearch) ([]core.Deployment, error) {
	items := []core.Deployment{}
	if len(filter.AppIDs) == 0 {
		return items, nil
	}
	if filter.Limit < 1 || filter.Limit > 101 {
		filter.Limit = 51
	}
	args := []any{}
	placeholders := make([]string, len(filter.AppIDs))
	for i, id := range filter.AppIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `SELECT d.id,d.app_id,d.commit_sha,d.spec_digest,d.state,'',d.created_at,d.started_at,d.finished_at,d.lease_until,'{}','{}' FROM deployments d JOIN apps a ON a.id=d.app_id WHERE d.app_id IN (` + strings.Join(placeholders, ",") + `)`
	if filter.Before != "" {
		query += ` AND (d.created_at < (SELECT created_at FROM deployments WHERE id=?) OR (d.created_at=(SELECT created_at FROM deployments WHERE id=?) AND d.id<?))`
		args = append(args, filter.Before, filter.Before, filter.Before)
	}
	if filter.State != "" {
		query += ` AND d.state=?`
		args = append(args, filter.State)
	}
	escape := func(value string) string {
		return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(value)) + "%"
	}
	if filter.Revision != "" {
		query += ` AND LOWER(d.commit_sha) LIKE ? ESCAPE '!'`
		args = append(args, escape(filter.Revision))
	}
	if filter.Query != "" {
		query += ` AND (LOWER(a.name) LIKE ? ESCAPE '!' OR LOWER(d.commit_sha) LIKE ? ESCAPE '!' OR LOWER(d.id) LIKE ? ESCAPE '!')`
		term := escape(filter.Query)
		args = append(args, term, term, term)
	}
	query += ` ORDER BY d.created_at DESC,d.id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

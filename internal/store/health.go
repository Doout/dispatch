package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) UpdateDeploymentHealth(ctx context.Context, id string, health core.DeploymentHealth) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE deployments SET health=? WHERE id=?`), jsonText(health), id)
	return changed(result, err)
}

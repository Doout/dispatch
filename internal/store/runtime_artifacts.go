package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

type RuntimeArtifactStore interface {
	SaveRuntimeArtifact(context.Context, core.RuntimeArtifact) error
	GetRuntimeArtifact(context.Context, string) (core.RuntimeArtifact, error)
}

func (s *SQLStore) SaveRuntimeArtifact(ctx context.Context, artifact core.RuntimeArtifact) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO deployment_runtime_artifacts(deployment_id,app_id,server_id,scope_id,ciphertext) VALUES(?,?,?,?,?)`), artifact.DeploymentID, artifact.AppID, artifact.ServerID, artifact.ScopeID, artifact.Ciphertext)
	return err
}

func (s *SQLStore) GetRuntimeArtifact(ctx context.Context, id string) (core.RuntimeArtifact, error) {
	var artifact core.RuntimeArtifact
	err := s.db.QueryRowContext(ctx, s.q(`SELECT deployment_id,app_id,server_id,scope_id,ciphertext FROM deployment_runtime_artifacts WHERE deployment_id=?`), id).Scan(&artifact.DeploymentID, &artifact.AppID, &artifact.ServerID, &artifact.ScopeID, &artifact.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return artifact, err
}

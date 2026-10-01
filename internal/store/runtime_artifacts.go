package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

type RuntimeArtifactStore interface {
	SaveRuntimeArtifact(context.Context, core.RuntimeArtifact) error
	GetRuntimeArtifact(context.Context, string) (core.RuntimeArtifact, error)
}

func (s *SQLStore) SaveRuntimeArtifact(ctx context.Context, artifact core.RuntimeArtifact) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO deployment_runtime_artifacts(deployment_id,app_id,server_id,scope_id,ciphertext,metadata) VALUES(?,?,?,?,?,?)`), artifact.DeploymentID, artifact.AppID, artifact.ServerID, artifact.ScopeID, artifact.Ciphertext, jsonText(artifact.Metadata))
	return err
}

func (s *SQLStore) GetRuntimeArtifact(ctx context.Context, id string) (core.RuntimeArtifact, error) {
	var artifact core.RuntimeArtifact
	var metadata string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT deployment_id,app_id,server_id,scope_id,ciphertext,metadata FROM deployment_runtime_artifacts WHERE deployment_id=?`), id).Scan(&artifact.DeploymentID, &artifact.AppID, &artifact.ServerID, &artifact.ScopeID, &artifact.Ciphertext, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(metadata), &artifact.Metadata)
	}
	return artifact, err
}

// ListRuntimeArtifacts includes retired metadata, but only private callers can
// access ciphertext. Public retention responses use a separate allowlisted DTO.
func (s *SQLStore) ListRuntimeArtifacts(ctx context.Context, server string) ([]core.RuntimeArtifact, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT deployment_id,app_id,server_id,scope_id,ciphertext,metadata FROM deployment_runtime_artifacts WHERE server_id=? ORDER BY deployment_id LIMIT 10001`), server)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.RuntimeArtifact{}
	for rows.Next() {
		var a core.RuntimeArtifact
		var metadata string
		if err = rows.Scan(&a.DeploymentID, &a.AppID, &a.ServerID, &a.ScopeID, &a.Ciphertext, &metadata); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(metadata), &a.Metadata); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) > 10000 {
		return nil, errors.New("runtime artifact inventory exceeds its safety limit")
	}
	return out, rows.Err()
}
func (s *SQLStore) RetireRuntimeArtifact(ctx context.Context, a core.RuntimeArtifact) error {
	a.Metadata.Retired = true
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE deployment_runtime_artifacts SET ciphertext='',metadata=? WHERE deployment_id=? AND app_id=? AND server_id=? AND scope_id=? AND ciphertext=?`), jsonText(a.Metadata), a.DeploymentID, a.AppID, a.ServerID, a.ScopeID, a.Ciphertext)
	return changed(result, err)
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) SaveDriftBaseline(ctx context.Context, b core.DriftBaseline) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO deployment_drift_baselines(deployment_id,app_id,server_id,namespace,release_name,ciphertext) VALUES(?,?,?,?,?,?)`), b.DeploymentID, b.AppID, b.ServerID, b.Namespace, b.Release, b.Ciphertext)
	return err
}
func (s *SQLStore) GetDriftBaseline(ctx context.Context, id string) (core.DriftBaseline, error) {
	var b core.DriftBaseline
	err := s.db.QueryRowContext(ctx, s.q(`SELECT deployment_id,app_id,server_id,namespace,release_name,ciphertext FROM deployment_drift_baselines WHERE deployment_id=?`), id).Scan(&b.DeploymentID, &b.AppID, &b.ServerID, &b.Namespace, &b.Release, &b.Ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return b, err
}
func (s *SQLStore) LatestSuccessfulDeployment(ctx context.Context, app string) (core.Deployment, error) {
	var id string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT id FROM deployments WHERE app_id=? AND state='succeeded' ORDER BY created_at DESC,id DESC LIMIT 1`), app).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Deployment{}, ErrNotFound
	}
	if err != nil {
		return core.Deployment{}, err
	}
	return s.GetDeployment(ctx, id)
}
func (s *SQLStore) SaveDriftCheck(ctx context.Context, app string, c core.DriftCheck) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO application_drift_checks(app_id,payload) VALUES(?,?) ON CONFLICT(app_id) DO UPDATE SET payload=excluded.payload`), app, jsonText(c))
	return err
}
func (s *SQLStore) GetDriftCheck(ctx context.Context, app string) (core.DriftCheck, error) {
	var c core.DriftCheck
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM application_drift_checks WHERE app_id=?`), app).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(raw), &c)
	return c, err
}
func (s *SQLStore) SaveDriftAction(ctx context.Context, a core.DriftAction) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO application_drift_actions(id,app_id,payload,created_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`), a.ID, a.AppID, jsonText(a), stamp(a.CreatedAt))
	return err
}
func (s *SQLStore) ListDriftActions(ctx context.Context, app string) ([]core.DriftAction, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM application_drift_actions WHERE app_id=? ORDER BY created_at DESC LIMIT 20`), app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.DriftAction{}
	for rows.Next() {
		var raw string
		var a core.DriftAction
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

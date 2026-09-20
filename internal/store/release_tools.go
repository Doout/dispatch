package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

type ReleaseStore interface {
	GetReleaseNote(context.Context, string) (core.ReleaseNote, error)
	SaveReleaseNote(context.Context, core.ReleaseNote, core.ReleaseAction) error
	SaveReleaseAction(context.Context, core.ReleaseAction) error
	ListReleaseActions(context.Context, string, string) ([]core.ReleaseAction, error)
	CreateRollbackDeployment(context.Context, core.Deployment, core.Deployment, core.ReleaseAction) error
}

func (s *SQLStore) GetReleaseNote(ctx context.Context, id string) (core.ReleaseNote, error) {
	var result core.ReleaseNote
	var payload string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM deployment_release_notes WHERE deployment_id=?`), id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ReleaseNote{DeploymentID: id, Links: []string{}}, nil
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal([]byte(payload), &result)
	return result, err
}

func (s *SQLStore) SaveReleaseNote(ctx context.Context, note core.ReleaseNote, action core.ReleaseAction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO deployment_release_notes(deployment_id,payload) VALUES(?,?) ON CONFLICT(deployment_id) DO UPDATE SET payload=excluded.payload`), note.DeploymentID, jsonText(note)); err != nil {
		return err
	}
	if err = s.insertReleaseAction(ctx, tx, action); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) insertReleaseAction(ctx context.Context, tx *changeTx, action core.ReleaseAction) error {
	_, err := tx.ExecContext(ctx, s.q(`INSERT INTO deployment_release_actions(id,project_id,app_id,deployment_id,source_deployment_id,payload,created_at) VALUES(?,?,?,?,?,?,?)`), action.ID, action.ProjectID, action.AppID, action.DeploymentID, action.SourceDeploymentID, jsonText(action), stamp(action.CreatedAt))
	return err
}

func (s *SQLStore) SaveReleaseAction(ctx context.Context, action core.ReleaseAction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.insertReleaseAction(ctx, tx, action); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) ListReleaseActions(ctx context.Context, project, app string) ([]core.ReleaseAction, error) {
	query := `SELECT payload FROM deployment_release_actions WHERE project_id=?`
	args := []any{project}
	if app != "" {
		query += ` AND app_id=?`
		args = append(args, app)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT 200`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ReleaseAction{}
	for rows.Next() {
		var raw string
		var item core.ReleaseAction
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CreateRollbackDeployment copies captured inputs in the same transaction as the
// audit and accepted run. It never captures the application's current bindings.
func (s *SQLStore) CreateRollbackDeployment(ctx context.Context, d, source core.Deployment, action core.ReleaseAction) error {
	if d.AppID != source.AppID || source.State != core.DeploymentSucceeded {
		return errors.New("rollback source must be a successful deployment of this application")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state, app string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT app_id,state FROM deployments WHERE id=?`), source.ID).Scan(&app, &state); err != nil {
		return err
	}
	if app != d.AppID || state != string(core.DeploymentSucceeded) {
		return errors.New("rollback source changed")
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO deployments(id,app_id,commit_sha,spec_digest,state,message,created_at,started_at,finished_at,lease_until,outputs,spec_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`), d.ID, d.AppID, source.CommitSHA, source.SpecDigest, string(d.State), d.Message, stamp(d.CreatedAt), nullTime(d.StartedAt), nullTime(d.FinishedAt), nullTime(d.LeaseUntil), jsonText(source.Outputs), jsonText(source.Snapshot))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO deployment_service_bindings(deployment_id,alias,service_id,payload) SELECT ?,alias,service_id,payload FROM deployment_service_bindings WHERE deployment_id=?`), d.ID, source.ID); err != nil {
		return err
	}
	if err = s.insertReleaseAction(ctx, tx, action); err != nil {
		return err
	}
	return tx.Commit()
}

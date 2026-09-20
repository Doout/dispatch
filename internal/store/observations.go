package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"time"
)

var ErrObservationConflict = errors.New("observation settings changed; refresh before saving")

func (s *SQLStore) GetObservationConfig(ctx context.Context, app string) (core.ObservationConfig, error) {
	var c core.ObservationConfig
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload,webhook_ciphertext FROM application_observation_configs WHERE app_id=?`), app).Scan(&raw, &c.WebhookCiphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c)
	}
	return c, err
}
func (s *SQLStore) SaveObservationConfig(ctx context.Context, c core.ObservationConfig, previous int64) error {
	if previous == 0 {
		result, err := s.db.ExecContext(ctx, s.q(`INSERT INTO application_observation_configs(app_id,project_id,revision,payload,webhook_ciphertext) VALUES(?,?,?,?,?) ON CONFLICT(app_id) DO NOTHING`), c.AppID, c.ProjectID, c.Revision, jsonText(c), c.WebhookCiphertext)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			return ErrObservationConflict
		}
		return err
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE application_observation_configs SET revision=?,payload=?,webhook_ciphertext=? WHERE app_id=? AND project_id=? AND revision=?`), c.Revision, jsonText(c), c.WebhookCiphertext, c.AppID, c.ProjectID, previous)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrObservationConflict
	}
	return err
}
func (s *SQLStore) ListObservationConfigs(ctx context.Context) ([]core.ObservationConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload,webhook_ciphertext FROM application_observation_configs ORDER BY app_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.ObservationConfig{}
	for rows.Next() {
		var c core.ObservationConfig
		var raw string
		if err = rows.Scan(&raw, &c.WebhookCiphertext); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (s *SQLStore) GetObservation(ctx context.Context, app string) (core.ApplicationObservation, error) {
	var c core.ApplicationObservation
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM application_observations WHERE app_id=?`), app).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &c)
	}
	return c, err
}
func (s *SQLStore) SaveObservation(ctx context.Context, result core.ApplicationObservation, event *core.ObservationEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revision int64
	var project string
	err = tx.QueryRowContext(ctx, s.q(`SELECT a.project_id,COALESCE(c.revision,0) FROM apps a LEFT JOIN application_observation_configs c ON c.app_id=a.id WHERE a.id=?`), result.AppID).Scan(&project, &revision)
	if err != nil {
		return err
	}
	if revision != result.ConfigurationRevision || project != result.ProjectID {
		return ErrObservationConflict
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO application_observations(app_id,payload) VALUES(?,?) ON CONFLICT(app_id) DO UPDATE SET payload=excluded.payload`), result.AppID, jsonText(result))
	if err != nil {
		return err
	}
	if event != nil {
		if err = s.insertObservationEvent(ctx, tx, *event); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *SQLStore) insertObservationEvent(ctx context.Context, tx *changeTx, e core.ObservationEvent) error {
	next := ""
	if e.NextAttemptAt != nil {
		next = stamp(*e.NextAttemptAt)
	}
	_, err := tx.ExecContext(ctx, s.q(`INSERT INTO application_observation_events(id,app_id,project_id,created_at,delivery,next_attempt_at,payload) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`), e.ID, e.AppID, e.ProjectID, stamp(e.CreatedAt), e.Delivery, next, jsonText(e))
	return err
}
func (s *SQLStore) SaveObservationEvent(ctx context.Context, e core.ObservationEvent) error {
	next := ""
	if e.NextAttemptAt != nil {
		next = stamp(*e.NextAttemptAt)
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO application_observation_events(id,app_id,project_id,created_at,delivery,next_attempt_at,payload) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET delivery=excluded.delivery,next_attempt_at=excluded.next_attempt_at,payload=excluded.payload`), e.ID, e.AppID, e.ProjectID, stamp(e.CreatedAt), e.Delivery, next, jsonText(e))
	return err
}
func (s *SQLStore) ListObservationEvents(ctx context.Context, app string) ([]core.ObservationEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM application_observation_events WHERE app_id=? ORDER BY created_at DESC,id DESC LIMIT 30`), app)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservationEvents(rows)
}
func (s *SQLStore) PendingObservationEvents(ctx context.Context, now time.Time) ([]core.ObservationEvent, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM application_observation_events WHERE delivery='pending' AND next_attempt_at<=? ORDER BY created_at LIMIT 20`), stamp(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanObservationEvents(rows)
}
func scanObservationEvents(rows *sql.Rows) ([]core.ObservationEvent, error) {
	out := []core.ObservationEvent{}
	for rows.Next() {
		var raw string
		var e core.ObservationEvent
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *SQLStore) CreateObservationEvent(ctx context.Context, e core.ObservationEvent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.insertObservationEvent(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"time"
)

type NeonStore interface {
	CreateNeonProvider(context.Context, core.NeonProvider) error
	GetNeonProvider(context.Context, string) (core.NeonProvider, error)
	ListNeonProviders(context.Context, string) ([]core.NeonProvider, error)
	CheckpointNeonResource(context.Context, core.ServiceResource) error
	GetNeonPreviewService(context.Context, string, string) (string, error)
}

func (s *SQLStore) CreateNeonProvider(ctx context.Context, p core.NeonProvider) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO neon_providers(id,project_id,payload) VALUES(?,?,?)`), p.ID, p.ProjectID, jsonText(p))
	return err
}
func (s *SQLStore) GetNeonProvider(ctx context.Context, id string) (core.NeonProvider, error) {
	var p core.NeonProvider
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM neon_providers WHERE id=?`), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p)
	return p, err
}
func (s *SQLStore) ListNeonProviders(ctx context.Context, project string) ([]core.NeonProvider, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT payload FROM neon_providers WHERE project_id=? ORDER BY id`), project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.NeonProvider{}
	for rows.Next() {
		var raw string
		var p core.NeonProvider
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// Checkpoints keep the original lease and revision, fencing a late worker after
// another controller has claimed recovery. They never extend the lease.
func (s *SQLStore) CheckpointNeonResource(ctx context.Context, r core.ServiceResource) error {
	if r.Target.Provider != "neon" || r.LeaseToken == "" || r.State != "recovering" {
		return ErrServiceResourceChanged
	}
	r.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE service_resources SET payload=? WHERE run_id=? AND revision=? AND lease_token=? AND state='recovering' AND lease_until>?`), jsonText(r), r.RunID, r.Revision, r.LeaseToken, stamp(r.UpdatedAt))
	return changed(result, err)
}

func (s *SQLStore) GetNeonPreviewService(ctx context.Context, preview, alias string) (string, error) {
	var run string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT run_id FROM neon_preview_services WHERE preview_id=? AND alias=?`), preview, alias).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return run, err
}

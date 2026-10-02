package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type NeonStore interface {
	CreateNeonProvider(context.Context, core.NeonProvider) error
	GetNeonProvider(context.Context, string) (core.NeonProvider, error)
	ListNeonProviders(context.Context, string) ([]core.NeonProvider, error)
	CheckpointNeonResource(context.Context, core.ServiceResource) error
	GetNeonPreviewService(context.Context, string, string) (string, error)
	DeleteUnusedNeonProvider(context.Context, string) error
}

func (s *SQLStore) DeleteUnusedNeonProvider(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `SELECT project_id FROM neon_providers WHERE id=?`
	if s.postgres {
		q += ` FOR UPDATE`
	}
	var project string
	if err = tx.QueryRowContext(ctx, s.q(q), id).Scan(&project); err != nil {
		return err
	}
	var n int
	// IDs are generated ULIDs. Conservative document matching also protects
	// malformed or disabled templates until their reference has been removed.
	for _, q := range []string{`SELECT COUNT(*) FROM service_resources WHERE project_id=? AND payload LIKE ?`, `SELECT COUNT(*) FROM saved_service_templates WHERE project_id=? AND document LIKE ?`, `SELECT COUNT(*) FROM workflow_resources w JOIN config_sources c ON c.id=w.config_source_id WHERE c.project_id=? AND w.document LIKE ?`} {
		if err = tx.QueryRowContext(ctx, s.q(q), project, "%"+id+"%").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrServiceInUse
		}
	}
	_, err = tx.ExecContext(ctx, s.q(`DELETE FROM neon_providers WHERE id=?`), id)
	if err != nil {
		return err
	}
	return tx.Commit()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if r.PreviewID != "" && r.ProviderPhase == "Creating isolated Neon branch" {
		if r.ReplacesRunID != "" {
			if err = s.checkNeonReplacement(ctx, tx, r, false); err != nil {
				return err
			}
		} else if err = s.lockPreviewResource(ctx, tx, r.PreviewID, false); err != nil {
			return err
		}
	}
	r.UpdatedAt = time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE service_resources SET payload=? WHERE run_id=? AND revision=? AND lease_token=? AND state='recovering' AND lease_until>?`), jsonText(r), r.RunID, r.Revision, r.LeaseToken, stamp(r.UpdatedAt))
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) GetNeonPreviewService(ctx context.Context, preview, alias string) (string, error) {
	var run string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT run_id FROM neon_preview_services WHERE preview_id=? AND alias=?`), preview, alias).Scan(&run)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return run, err
}

func validServicePolicy(r core.ServiceResource) bool {
	return r.Policy == "retain" || r.Target.Provider == "neon" && (r.Policy == "suspend" || r.Policy == "delete")
}

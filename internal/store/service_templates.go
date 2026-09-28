package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

var ErrServiceTemplateConflict = errors.New("template changed; reload and retry")

func (s *SQLStore) CreateSavedServiceTemplate(ctx context.Context, item core.SavedServiceTemplate) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO saved_service_templates(id,project_id,config_source_id,name,document,digest,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`), item.ID, item.ProjectID, nullString(item.ConfigSourceID), item.Name, item.Document, item.Digest, item.Revision, stamp(item.CreatedAt), stamp(item.UpdatedAt))
	return s.serviceTemplateWriteError(ctx, item, err)
}

func (s *SQLStore) serviceTemplateWriteError(ctx context.Context, item core.SavedServiceTemplate, err error) error {
	if err == nil {
		return nil
	}
	var id string
	if lookup := s.db.QueryRowContext(ctx, s.q(`SELECT id FROM saved_service_templates WHERE project_id=? AND name=?`), item.ProjectID, item.Name).Scan(&id); lookup == nil && id != item.ID {
		return ErrAlreadyExists
	}
	return err
}

func (s *SQLStore) UpdateSavedServiceTemplate(ctx context.Context, item core.SavedServiceTemplate, expected int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE saved_service_templates SET config_source_id=?,name=?,document=?,digest=?,revision=?,updated_at=? WHERE id=? AND project_id=? AND revision=?`), nullString(item.ConfigSourceID), item.Name, item.Document, item.Digest, item.Revision, stamp(item.UpdatedAt), item.ID, item.ProjectID, expected)
	if err != nil {
		return s.serviceTemplateWriteError(ctx, item, err)
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrServiceTemplateConflict
	}
	return err
}

const savedServiceTemplateSelect = `SELECT id,project_id,config_source_id,name,document,digest,revision,created_at,updated_at FROM saved_service_templates`

func scanSavedServiceTemplate(row scanner) (core.SavedServiceTemplate, error) {
	var item core.SavedServiceTemplate
	var source sql.NullString
	var created, updated string
	err := row.Scan(&item.ID, &item.ProjectID, &source, &item.Name, &item.Document, &item.Digest, &item.Revision, &created, &updated)
	item.ConfigSourceID, item.CreatedAt, item.UpdatedAt = source.String, parseTime(created), parseTime(updated)
	return item, err
}

func (s *SQLStore) GetSavedServiceTemplate(ctx context.Context, id string) (core.SavedServiceTemplate, error) {
	item, err := scanSavedServiceTemplate(s.db.QueryRowContext(ctx, s.q(savedServiceTemplateSelect+` WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListSavedServiceTemplates(ctx context.Context) ([]core.SavedServiceTemplate, error) {
	rows, err := s.db.QueryContext(ctx, savedServiceTemplateSelect+` ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.SavedServiceTemplate{}
	for rows.Next() {
		item, err := scanSavedServiceTemplate(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) DeleteSavedServiceTemplate(ctx context.Context, id string, revision int64) error {
	result, err := s.db.ExecContext(ctx, s.q(`DELETE FROM saved_service_templates WHERE id=? AND revision=?`), id, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrServiceTemplateConflict
	}
	return err
}

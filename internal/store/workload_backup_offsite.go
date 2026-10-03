package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
)

type BackupObjectStoreStore interface {
	CreateBackupObjectStore(context.Context, core.BackupObjectStore) error
	GetBackupObjectStore(context.Context, string) (core.BackupObjectStore, error)
	ListBackupObjectStores(context.Context) ([]core.BackupObjectStore, error)
}

func (s *SQLStore) CreateBackupObjectStore(ctx context.Context, item core.BackupObjectStore) error {
	if err := item.Config.Validate(); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO workload_backup_object_stores(id,project_id,credential_secret_id,payload) VALUES(?,?,?,?)`), item.ID, item.ProjectID, item.CredentialSecretID, jsonText(item))
	return err
}
func (s *SQLStore) GetBackupObjectStore(ctx context.Context, id string) (core.BackupObjectStore, error) {
	var item core.BackupObjectStore
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM workload_backup_object_stores WHERE id=?`), id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &item)
	}
	return item, err
}
func (s *SQLStore) ListBackupObjectStores(ctx context.Context) ([]core.BackupObjectStore, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM workload_backup_object_stores ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.BackupObjectStore{}
	for rows.Next() {
		var raw string
		var item core.BackupObjectStore
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

// SetBackupObjectStoreConditionalDelete changes only the declared deletion
// capability. Destination and credential identity remain fixed for old exports.
func (s *SQLStore) SetBackupObjectStoreConditionalDelete(ctx context.Context, id string, before bool, enabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	query := `SELECT payload FROM workload_backup_object_stores WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var raw string
	var item core.BackupObjectStore
	if err = tx.QueryRowContext(ctx, s.q(query), id).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal([]byte(raw), &item); err != nil {
		return err
	}
	if item.Config.ConditionalDelete != before {
		return ErrWorkloadBackupChanged
	}
	var active int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM workload_backup_operations WHERE offsite_store_id=? AND state IN ('running','unknown')`), id).Scan(&active); err != nil {
		return err
	}
	if active > 0 {
		return ErrWorkloadBackupChanged
	}
	item.Config.ConditionalDelete = enabled
	result, err := tx.ExecContext(ctx, s.q(`UPDATE workload_backup_object_stores SET payload=? WHERE id=? AND payload=?`), jsonText(item), id, raw)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

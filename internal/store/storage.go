package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrStorageChanged = errors.New("storage changed; reconcile and review again")
var ErrStorageProtected = errors.New("target still has retained, in-use or unverified storage; preserve its backing data before removing the target")

type StorageStore interface {
	ListStorage(context.Context, string) ([]core.StorageResource, error)
	GetStorage(context.Context, string) (core.StorageResource, error)
	ObserveStorage(context.Context, core.StorageResource) error
	SetStoragePolicy(context.Context, string, int64, string) error
}

func (s *SQLStore) ListStorage(ctx context.Context, serverID string) ([]core.StorageResource, error) {
	query := `SELECT payload,revision FROM storage_resources`
	args := []any{}
	if serverID != "" {
		query += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	rows, err := s.db.QueryContext(ctx, s.q(query+` ORDER BY id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.StorageResource{}
	for rows.Next() {
		var payload string
		var item core.StorageResource
		var revision int64
		if err := rows.Scan(&payload, &revision); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		item.Revision = revision
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) GetStorage(ctx context.Context, id string) (core.StorageResource, error) {
	var payload string
	var item core.StorageResource
	var revision int64
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload,revision FROM storage_resources WHERE id=?`), id).Scan(&payload, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	err = json.Unmarshal([]byte(payload), &item)
	item.Revision = revision
	return item, err
}

// ObserveStorage preserves operator policy only while ownership and runtime identity agree.
// Observation timestamps alone do not invalidate a destructive review.
func (s *SQLStore) ObserveStorage(ctx context.Context, item core.StorageResource) error {
	old, err := s.GetStorage(ctx, item.ID)
	if errors.Is(err, ErrNotFound) {
		item.Policy, item.Revision = "retain", 1
		_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO storage_resources(id,server_id,revision,payload) VALUES(?,?,?,?)`), item.ID, item.ServerID, item.Revision, jsonText(item))
		return err
	}
	if err != nil {
		return err
	}
	item.Policy, item.Revision = old.Policy, old.Revision
	if old.Identity != item.Identity || old.OwnerID != item.OwnerID || old.OwnerKind != item.OwnerKind || old.ProjectID != item.ProjectID || old.Ownership != item.Ownership {
		item.Policy = "retain"
	}
	x, y := old, item
	x.ObservedAt, y.ObservedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(x, y) {
		item.Revision++
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE storage_resources SET payload=?,revision=? WHERE id=? AND revision=?`), jsonText(item), item.Revision, item.ID, old.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrStorageChanged
	}
	return err
}

func (s *SQLStore) SetStoragePolicy(ctx context.Context, id string, expected int64, policy string) error {
	if policy != "retain" && policy != "destroy" {
		return errors.New("storage policy must be retain or destroy")
	}
	item, err := s.GetStorage(ctx, id)
	if err != nil {
		return err
	}
	if item.Revision != expected {
		return ErrStorageChanged
	}
	if item.Ownership != "verified" {
		return ErrStorageProtected
	}
	item.Policy, item.Revision = policy, expected+1
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE storage_resources SET payload=?,revision=? WHERE id=? AND revision=?`), jsonText(item), item.Revision, id, expected)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrStorageChanged
	}
	return err
}

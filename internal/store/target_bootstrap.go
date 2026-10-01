package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

type TargetBootstrapStore interface {
	CreateTargetBootstrap(context.Context, core.TargetBootstrap) error
	GetTargetBootstrap(context.Context, string) (core.TargetBootstrap, error)
	ListTargetBootstraps(context.Context, string) ([]core.TargetBootstrap, error)
	UpdateTargetBootstrap(context.Context, core.TargetBootstrap, int64) error
	IssueBootstrapEnrollment(context.Context, core.TargetBootstrap, int64, core.EdgeCredential, int64) error
}

func (s *SQLStore) CreateTargetBootstrap(ctx context.Context, item core.TargetBootstrap) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO target_bootstraps(id,server_id,revision,record,claim_hash,encrypted_input) VALUES(?,?,1,?,?,?)`), item.ID, item.ServerID, jsonText(item), item.ClaimHash, item.EncryptedInput)
	return err
}
func (s *SQLStore) GetTargetBootstrap(ctx context.Context, id string) (core.TargetBootstrap, error) {
	var item core.TargetBootstrap
	var record string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT record,revision,claim_hash,encrypted_input FROM target_bootstraps WHERE id=?`), id).Scan(&record, &item.Revision, &item.ClaimHash, &item.EncryptedInput)
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	if err != nil {
		return item, err
	}
	revision, hash, encrypted := item.Revision, item.ClaimHash, item.EncryptedInput
	err = json.Unmarshal([]byte(record), &item)
	item.Revision, item.ClaimHash, item.EncryptedInput = revision, hash, encrypted
	return item, err
}
func (s *SQLStore) ListTargetBootstraps(ctx context.Context, serverID string) ([]core.TargetBootstrap, error) {
	query := `SELECT record,revision FROM target_bootstraps`
	args := []any{}
	if serverID != "" {
		query += ` WHERE server_id=?`
		args = append(args, serverID)
	}
	query += ` ORDER BY id DESC`
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.TargetBootstrap{}
	for rows.Next() {
		var item core.TargetBootstrap
		var record string
		var revision int64
		if err = rows.Scan(&record, &revision); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(record), &item); err != nil {
			return nil, err
		}
		item.Revision = revision
		result = append(result, item)
	}
	return result, rows.Err()
}
func (s *SQLStore) UpdateTargetBootstrap(ctx context.Context, item core.TargetBootstrap, previous int64) error {
	item.Revision = previous + 1
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE target_bootstraps SET revision=?,record=?,claim_hash=?,encrypted_input=?,active_server_id=? WHERE id=? AND revision=?`), item.Revision, jsonText(item), item.ClaimHash, item.EncryptedInput, bootstrapActiveServer(item), item.ID, previous)
	return changed(result, err)
}

func bootstrapActiveServer(item core.TargetBootstrap) any {
	switch item.State {
	case "accepted", "installing", "waiting", "unknown":
		return item.ServerID
	}
	return nil
}

// IssueBootstrapEnrollment commits the fresh one-use token and its encrypted
// retry receipt together. A lost HTTP response cannot lose the token or rotate
// an identity again. The reviewed credential generation must still match.
func (s *SQLStore) IssueBootstrapEnrollment(ctx context.Context, item core.TargetBootstrap, previous int64, credential core.EdgeCredential, expectedGeneration int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expectedGeneration == 0 {
		result, e := tx.ExecContext(ctx, s.q(`INSERT INTO edge_node_credentials(network_id,generation,enrollment_hash,enrollment_expires_at,public_key,session_hash,session_expires_at,revoked,updated_at) VALUES(?,1,?,?,'','',?,FALSE,?) ON CONFLICT(network_id) DO NOTHING`), credential.NetworkID, credential.EnrollmentHash, stamp(credential.EnrollmentExpiresAt), stamp(credential.SessionExpiresAt), stamp(credential.UpdatedAt))
		if e = changed(result, e); e != nil {
			return e
		}
	} else {
		result, e := tx.ExecContext(ctx, s.q(`UPDATE edge_node_credentials SET generation=generation+1,enrollment_hash=?,enrollment_expires_at=?,public_key='',session_hash='',session_expires_at=?,revoked=FALSE,updated_at=? WHERE network_id=? AND generation=? AND (public_key='' OR ?)`), credential.EnrollmentHash, stamp(credential.EnrollmentExpiresAt), stamp(credential.SessionExpiresAt), stamp(credential.UpdatedAt), credential.NetworkID, expectedGeneration, item.Plan.ReplaceIdentity)
		if e = changed(result, e); e != nil {
			return e
		}
	}
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM edge_node_challenges WHERE network_id=?`), credential.NetworkID); err != nil {
		return err
	}
	item.Revision = previous + 1
	result, err := tx.ExecContext(ctx, s.q(`UPDATE target_bootstraps SET revision=?,record=?,encrypted_input=?,active_server_id=? WHERE id=? AND revision=?`), item.Revision, jsonText(item), item.EncryptedInput, bootstrapActiveServer(item), item.ID, previous)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

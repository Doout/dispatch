package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type TargetBootstrapStore interface {
	CreateTargetBootstrap(context.Context, core.TargetBootstrap) error
	AcceptTargetBootstrap(context.Context, core.TargetBootstrap, int64) error
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
	// Recheck the actual allocation inside credential issuance, including the
	// immutable provider resource selected at the protected claim boundary.
	query := `SELECT id FROM servers WHERE id=? AND agent_node_id=? AND project_id=? AND address=?`
	args := []any{item.ServerID, item.NodeID, item.ProjectID, item.Plan.SSHHost}
	if item.ProviderID != "" {
		query = `SELECT id FROM managed_servers WHERE id=? AND node_id=? AND project_id=? AND provider_id=? AND review_id=? AND resource_id=? AND bootstrap_id=? AND allocation_state='allocated'`
		args = []any{item.ServerID, item.NodeID, item.ProjectID, item.ProviderID, item.ReviewID, item.ResourceID, item.ID}
	}
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var bound string
	if err = tx.QueryRowContext(ctx, s.q(query), args...).Scan(&bound); err != nil {
		return ErrInfrastructureChanged
	}
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

// Acceptance activates the inert claim in the same transaction that records the
// provider operation. The rendered provider request remains byte-for-byte fixed.
func (s *SQLStore) acceptProviderBootstrap(ctx context.Context, tx *sql.Tx, r core.InfrastructureReview, server core.ManagedServer, now time.Time) error {
	var record string
	var revision int64
	if err := tx.QueryRowContext(ctx, s.q(`SELECT record,revision FROM target_bootstraps WHERE id=?`), r.BootstrapID).Scan(&record, &revision); err != nil {
		return err
	}
	var item core.TargetBootstrap
	if json.Unmarshal([]byte(record), &item) != nil || item.State != "planned" || item.Plan.Method != "cloud_init" || item.ReviewID != r.ID || item.ServerID != server.ID || item.NodeID != server.NodeID || item.ProviderID != r.ProviderID || item.ProjectID != r.ProjectID || !item.ReviewExpiresAt.After(now) {
		return ErrInfrastructureChanged
	}
	item.AcceptedAt = &now
	item.UpdatedAt = now
	item.Revision = revision + 1
	item.State = "accepted"
	item.Message = "Installation approved; waiting for the intended provider resource."
	result, err := tx.ExecContext(ctx, s.q(`UPDATE target_bootstraps SET record=?,revision=?,active_server_id=? WHERE id=? AND revision=?`), jsonText(item), item.Revision, item.ServerID, item.ID, revision)
	return changed(result, err)
}

// AcceptTargetBootstrap supersedes the previous installer claim atomically. A
// fresh SSH recovery approval can retire a stuck cloud-init plan without letting
// both installers refresh enrollment for the same machine.
func (s *SQLStore) AcceptTargetBootstrap(ctx context.Context, item core.TargetBootstrap, previous int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int64
	err = tx.QueryRowContext(ctx, s.q(`SELECT generation FROM edge_node_credentials WHERE network_id=?`), item.NodeID).Scan(&generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if generation != item.ExpectedGeneration {
		return ErrInfrastructureChanged
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT id,record,revision FROM target_bootstraps WHERE server_id=? AND id<>?`), item.ServerID, item.ID)
	if err != nil {
		return err
	}
	var oldItems []core.TargetBootstrap
	for rows.Next() {
		var old core.TargetBootstrap
		var raw string
		var rev int64
		if err = rows.Scan(&old.ID, &raw, &rev); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &old); err != nil {
			rows.Close()
			return err
		}
		old.Revision = rev
		oldItems = append(oldItems, old)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, old := range oldItems {
		if old.AcceptedAt == nil || old.State == "cancelled" || old.State == "failed" {
			continue
		}
		// An executing SSH installer cannot safely be superseded until its bounded lease ends.
		if old.LeaseUntil != nil && old.LeaseUntil.After(item.UpdatedAt) {
			return ErrInfrastructureChanged
		}
		old.State = "cancelled"
		old.Message = "Superseded by a separately reviewed target installation."
		old.Revision++
		old.UpdatedAt = item.UpdatedAt
		result, e := tx.ExecContext(ctx, s.q(`UPDATE target_bootstraps SET active_server_id=NULL,record=?,revision=? WHERE id=? AND revision=?`), jsonText(old), old.Revision, old.ID, old.Revision-1)
		if e = changed(result, e); e != nil {
			return e
		}
	}
	if item.ProviderID != "" && item.Plan.Method == "ssh" {
		result, e := tx.ExecContext(ctx, s.q(`UPDATE managed_servers SET bootstrap_id=?,revision=revision+1,updated_at=? WHERE id=? AND review_id=? AND node_id=? AND provider_id=? AND project_id=? AND allocation_state='allocated'`), item.ID, stamp(item.UpdatedAt), item.ServerID, item.ReviewID, item.NodeID, item.ProviderID, item.ProjectID)
		if e = changed(result, e); e != nil {
			return e
		}
	}
	item.Revision = previous + 1
	result, err := tx.ExecContext(ctx, s.q(`UPDATE target_bootstraps SET active_server_id=?,record=?,revision=? WHERE id=? AND revision=?`), item.ServerID, jsonText(item), item.Revision, item.ID, previous)
	if err = changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

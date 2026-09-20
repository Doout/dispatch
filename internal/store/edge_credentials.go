package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrEdgeCredential = errors.New("edge credential is invalid, expired or revoked")

func (s *SQLStore) GetEdgeCredential(ctx context.Context, id string) (core.EdgeCredential, error) {
	var c core.EdgeCredential
	var enrollment, session, updated string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT network_id,generation,enrollment_hash,enrollment_expires_at,public_key,session_hash,session_expires_at,revoked,updated_at FROM edge_node_credentials WHERE network_id=?`), id).Scan(&c.NetworkID, &c.Generation, &c.EnrollmentHash, &enrollment, &c.PublicKey, &c.SessionHash, &session, &c.Revoked, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.EnrollmentExpiresAt, _ = time.Parse(time.RFC3339Nano, enrollment)
	c.SessionExpiresAt, _ = time.Parse(time.RFC3339Nano, session)
	c.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return c, nil
}
func (s *SQLStore) RotateEdgeCredential(ctx context.Context, c core.EdgeCredential) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO edge_node_credentials(network_id,generation,enrollment_hash,enrollment_expires_at,public_key,session_hash,session_expires_at,revoked,updated_at) VALUES(?,1,?,?,'','',?,FALSE,?) ON CONFLICT(network_id) DO UPDATE SET generation=edge_node_credentials.generation+1,enrollment_hash=excluded.enrollment_hash,enrollment_expires_at=excluded.enrollment_expires_at,public_key='',session_hash='',session_expires_at=excluded.session_expires_at,revoked=FALSE,updated_at=excluded.updated_at`), c.NetworkID, c.EnrollmentHash, stamp(c.EnrollmentExpiresAt), stamp(time.Time{}), stamp(c.UpdatedAt))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`DELETE FROM edge_node_challenges WHERE network_id=?`), c.NetworkID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) EnrollEdgeCredential(ctx context.Context, id, hash, publicKey, sessionHash string, now, expires time.Time) error {
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE edge_node_credentials SET enrollment_hash='',public_key=?,session_hash=?,session_expires_at=?,updated_at=? WHERE network_id=? AND revoked=FALSE AND public_key='' AND enrollment_hash=? AND enrollment_expires_at>?`), publicKey, sessionHash, stamp(expires), stamp(now), id, hash, stamp(now))
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrEdgeCredential
	}
	return err
}
func (s *SQLStore) CreateEdgeChallenge(ctx context.Context, id, hash string, generation int64, now, expires time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM edge_node_challenges WHERE expires_at<=?`), stamp(now)); err != nil {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM edge_node_challenges WHERE network_id=?`), id).Scan(&count); err != nil {
		return err
	}
	if count >= 8 {
		return ErrEdgeCredential
	}
	result, err := tx.ExecContext(ctx, s.q(`INSERT INTO edge_node_challenges(challenge_hash,network_id,generation,expires_at) SELECT ?,network_id,generation,? FROM edge_node_credentials WHERE network_id=? AND generation=? AND revoked=FALSE AND public_key<>''`), hash, stamp(expires), id, generation)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEdgeCredential
	}
	return tx.Commit()
}
func (s *SQLStore) ExchangeEdgeChallenge(ctx context.Context, id, challengeHash, publicKey, sessionHash string, generation int64, now, expires time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var consumed string
	err = tx.QueryRowContext(ctx, s.q(`DELETE FROM edge_node_challenges WHERE challenge_hash=? AND network_id=? AND generation=? AND expires_at>? RETURNING challenge_hash`), challengeHash, id, generation, stamp(now)).Scan(&consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEdgeCredential
	}
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`UPDATE edge_node_credentials SET session_hash=?,session_expires_at=?,updated_at=? WHERE network_id=? AND generation=? AND public_key=? AND revoked=FALSE`), sessionHash, stamp(expires), stamp(now), id, generation, publicKey)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrEdgeCredential
	}
	return tx.Commit()
}
func (s *SQLStore) RevokeEdgeCredential(ctx context.Context, id string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A row is also created for legacy nodes, so an old token can never bypass revocation.
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO edge_node_credentials(network_id,generation,enrollment_hash,enrollment_expires_at,public_key,session_hash,session_expires_at,revoked,updated_at) VALUES(?,1,'',?,'','',?,TRUE,?) ON CONFLICT(network_id) DO UPDATE SET generation=edge_node_credentials.generation+1,enrollment_hash='',session_hash='',revoked=TRUE,updated_at=excluded.updated_at`), id, stamp(now), stamp(now), stamp(now))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`DELETE FROM edge_node_challenges WHERE network_id=?`), id)
	if err != nil {
		return err
	}
	return tx.Commit()
}

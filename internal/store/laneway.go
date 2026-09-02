package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrLanewayRefreshBusy = errors.New("Laneway credential refresh is already in progress")

func (s *SQLStore) CreateLanewayApplication(ctx context.Context, item core.LanewayApplication) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO laneway_applications(id,name,authority,remote_application_id,client_id,encrypted_client_secret,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`),
		item.ID, item.Name, item.Authority, item.RemoteApplicationID, item.ClientID, item.EncryptedClientSecret, item.State, stamp(item.CreatedAt), stamp(item.UpdatedAt))
	return err
}

func (s *SQLStore) GetLanewayApplication(ctx context.Context, id string) (core.LanewayApplication, error) {
	item, err := scanLanewayApplication(s.db.QueryRowContext(ctx, s.q(`SELECT id,name,authority,remote_application_id,client_id,encrypted_client_secret,state,created_at,updated_at FROM laneway_applications WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) GetActiveLanewayApplicationByAuthority(ctx context.Context, authority string) (core.LanewayApplication, error) {
	item, err := scanLanewayApplication(s.db.QueryRowContext(ctx, s.q(`SELECT id,name,authority,remote_application_id,client_id,encrypted_client_secret,state,created_at,updated_at FROM laneway_applications WHERE authority=? AND state='active' ORDER BY created_at LIMIT 1`), authority))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func scanLanewayApplication(row scanner) (core.LanewayApplication, error) {
	var item core.LanewayApplication
	var created, updated string
	err := row.Scan(&item.ID, &item.Name, &item.Authority, &item.RemoteApplicationID, &item.ClientID, &item.EncryptedClientSecret, &item.State, &created, &updated)
	if err != nil {
		return item, err
	}
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	return item, nil
}

func (s *SQLStore) CreateLanewayAuthorizationTransaction(ctx context.Context, item core.LanewayAuthorizationTransaction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM laneway_authorization_transactions WHERE expires_at<=? OR consumed_at IS NOT NULL`), stamp(item.CreatedAt)); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO laneway_authorization_transactions(state_hash,kind,connection_name,authority,redirect_uri,application_id,encrypted_code_verifier,initiating_user_id,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`),
		item.StateHash, item.Kind, item.ConnectionName, item.Authority, item.RedirectURI, nullableString(item.ApplicationID), item.EncryptedCodeVerifier, item.InitiatingUserID, stamp(item.ExpiresAt), stamp(item.CreatedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) ConsumeLanewayAuthorizationTransaction(ctx context.Context, stateHash string, now time.Time) (core.LanewayAuthorizationTransaction, error) {
	var item core.LanewayAuthorizationTransaction
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return item, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, s.q(`UPDATE laneway_authorization_transactions SET consumed_at=? WHERE state_hash=? AND consumed_at IS NULL AND expires_at>?`), stamp(now), stateHash, stamp(now))
	if err != nil {
		return item, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return item, err
	}
	if rows != 1 {
		return item, ErrNotFound
	}
	item, err = scanLanewayAuthorizationTransaction(tx.QueryRowContext(ctx, s.q(`SELECT state_hash,kind,connection_name,authority,redirect_uri,application_id,encrypted_code_verifier,initiating_user_id,expires_at,consumed_at,created_at FROM laneway_authorization_transactions WHERE state_hash=?`), stateHash))
	if err != nil {
		return item, err
	}
	if err := tx.Commit(); err != nil {
		return item, err
	}
	return item, nil
}

func scanLanewayAuthorizationTransaction(row scanner) (core.LanewayAuthorizationTransaction, error) {
	var item core.LanewayAuthorizationTransaction
	var applicationID, consumed sql.NullString
	var expires, created string
	err := row.Scan(&item.StateHash, &item.Kind, &item.ConnectionName, &item.Authority, &item.RedirectURI, &applicationID, &item.EncryptedCodeVerifier, &item.InitiatingUserID, &expires, &consumed, &created)
	if err != nil {
		return item, err
	}
	item.ApplicationID = applicationID.String
	item.ExpiresAt, item.ConsumedAt, item.CreatedAt = parseTime(expires), parseNullTime(consumed), parseTime(created)
	return item, nil
}

func (s *SQLStore) AcquireLanewayRefreshLease(ctx context.Context, networkID, leaseToken string, now, leaseUntil time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM laneway_refresh_leases WHERE private_network_id=? AND lease_until<=?`), networkID, stamp(now)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`INSERT INTO laneway_refresh_leases(private_network_id,lease_token,lease_until) VALUES(?,?,?) ON CONFLICT(private_network_id) DO NOTHING`), networkID, leaseToken, stamp(leaseUntil))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrLanewayRefreshBusy
	}
	return tx.Commit()
}

func (s *SQLStore) ReleaseLanewayRefreshLease(ctx context.Context, networkID, leaseToken string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM laneway_refresh_leases WHERE private_network_id=? AND lease_token=?`), networkID, leaseToken)
	return err
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

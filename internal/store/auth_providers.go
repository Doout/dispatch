package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) CreateAuthProvider(ctx context.Context, item core.AuthProvider) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO auth_providers(id,name,provider_type,base_url,api_url,client_id,encrypted_client_secret,provisioning,state,last_verified_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`),
		item.ID, item.Name, item.Type, item.BaseURL, item.APIURL, item.ClientID, item.EncryptedClientSecret, item.Provisioning, item.State, nullTime(item.LastVerifiedAt), stamp(item.CreatedAt), stamp(item.UpdatedAt))
	return err
}

func (s *SQLStore) UpdateAuthProvider(ctx context.Context, item core.AuthProvider) error {
	return changed(s.db.ExecContext(ctx, s.q(`UPDATE auth_providers SET name=?,provider_type=?,base_url=?,api_url=?,client_id=?,encrypted_client_secret=?,provisioning=?,state=?,last_verified_at=?,updated_at=? WHERE id=?`),
		item.Name, item.Type, item.BaseURL, item.APIURL, item.ClientID, item.EncryptedClientSecret, item.Provisioning, item.State, nullTime(item.LastVerifiedAt), stamp(item.UpdatedAt), item.ID))
}

func (s *SQLStore) DeleteAuthProvider(ctx context.Context, id string) error {
	return changed(s.db.ExecContext(ctx, s.q(`DELETE FROM auth_providers WHERE id=?`), id))
}

func (s *SQLStore) GetAuthProvider(ctx context.Context, id string) (core.AuthProvider, error) {
	item, err := scanAuthProvider(s.db.QueryRowContext(ctx, s.q(`SELECT id,name,provider_type,base_url,api_url,client_id,encrypted_client_secret,provisioning,state,last_verified_at,created_at,updated_at FROM auth_providers WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) ListAuthProviders(ctx context.Context) ([]core.AuthProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,provider_type,base_url,api_url,client_id,encrypted_client_secret,provisioning,state,last_verified_at,created_at,updated_at FROM auth_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.AuthProvider{}
	for rows.Next() {
		item, err := scanAuthProvider(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanAuthProvider(row scanner) (core.AuthProvider, error) {
	var item core.AuthProvider
	var created, updated string
	var verified sql.NullString
	err := row.Scan(&item.ID, &item.Name, &item.Type, &item.BaseURL, &item.APIURL, &item.ClientID, &item.EncryptedClientSecret, &item.Provisioning, &item.State, &verified, &created, &updated)
	if err != nil {
		return item, err
	}
	item.ClientSecretConfigured = item.EncryptedClientSecret != ""
	item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
	if verified.Valid {
		value := parseTime(verified.String)
		item.LastVerifiedAt = &value
	}
	return item, nil
}

func (s *SQLStore) GetExternalIdentity(ctx context.Context, providerID, subject string) (core.ExternalIdentity, error) {
	item, err := scanExternalIdentity(s.db.QueryRowContext(ctx, s.q(`SELECT provider_id,subject,user_id,login,email,last_login_at,created_at FROM external_identities WHERE provider_id=? AND subject=?`), providerID, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return item, ErrNotFound
	}
	return item, err
}

func (s *SQLStore) FindExternalIdentitiesByLogin(ctx context.Context, login string) ([]core.ExternalIdentity, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT provider_id,subject,user_id,login,email,last_login_at,created_at FROM external_identities WHERE LOWER(login)=LOWER(?) OR LOWER(email)=LOWER(?) ORDER BY provider_id`), login, login)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ExternalIdentity{}
	for rows.Next() {
		item, err := scanExternalIdentity(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) ListExternalIdentities(ctx context.Context) ([]core.ExternalIdentity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT provider_id,subject,user_id,login,email,last_login_at,created_at FROM external_identities ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ExternalIdentity{}
	for rows.Next() {
		item, err := scanExternalIdentity(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) UpsertExternalIdentity(ctx context.Context, item core.ExternalIdentity) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO external_identities(provider_id,subject,user_id,login,email,last_login_at,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider_id,subject) DO UPDATE SET user_id=excluded.user_id,login=excluded.login,email=excluded.email,last_login_at=excluded.last_login_at`),
		item.ProviderID, item.Subject, item.UserID, item.Login, item.Email, stamp(item.LastLogin), stamp(item.CreatedAt))
	return err
}

func (s *SQLStore) DeleteExternalIdentity(ctx context.Context, providerID, userID string) error {
	return changed(s.db.ExecContext(ctx, s.q(`DELETE FROM external_identities WHERE provider_id=? AND user_id=?`), providerID, userID))
}

func scanExternalIdentity(row scanner) (core.ExternalIdentity, error) {
	var item core.ExternalIdentity
	var lastLogin, created string
	err := row.Scan(&item.ProviderID, &item.Subject, &item.UserID, &item.Login, &item.Email, &lastLogin, &created)
	item.LastLogin, item.CreatedAt = parseTime(lastLogin), parseTime(created)
	return item, err
}

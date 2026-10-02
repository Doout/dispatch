package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrProviderChanged = errors.New("provider registration changed; reload and review it again")

type InfrastructureProviderStore interface {
	CreateInfrastructureProvider(context.Context, core.InfrastructureProvider) error
	UpdateInfrastructureProvider(context.Context, core.InfrastructureProvider, int64) error
	GetInfrastructureProvider(context.Context, string) (core.InfrastructureProvider, error)
	ListInfrastructureProviders(context.Context) ([]core.InfrastructureProvider, error)
}

const infrastructureProviderColumns = `id,name,endpoint,private_network_id,credential_secret_id,enabled,capabilities,manifest,manifest_digest,state,last_error,revision,last_verified_at,created_at,updated_at`

func (s *SQLStore) CreateInfrastructureProvider(ctx context.Context, p core.InfrastructureProvider) error {
	caps, err := json.Marshal(p.Capabilities)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO infrastructure_providers(`+infrastructureProviderColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`), p.ID, p.Name, p.Endpoint, nullString(p.PrivateNetworkID), nullString(p.CredentialSecretID), p.Enabled, string(caps), string(p.Manifest), p.ManifestDigest, p.State, p.LastError, p.Revision, nullableProviderTime(p.LastVerifiedAt), stamp(p.CreatedAt), stamp(p.UpdatedAt))
	return err
}

func (s *SQLStore) UpdateInfrastructureProvider(ctx context.Context, p core.InfrastructureProvider, expected int64) error {
	caps, err := json.Marshal(p.Capabilities)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE infrastructure_providers SET name=?,credential_secret_id=?,enabled=?,capabilities=?,manifest=?,manifest_digest=?,state=?,last_error=?,revision=?,last_verified_at=?,updated_at=? WHERE id=? AND revision=? AND endpoint=? AND COALESCE(private_network_id,'')=?`), p.Name, nullString(p.CredentialSecretID), p.Enabled, string(caps), string(p.Manifest), p.ManifestDigest, p.State, p.LastError, p.Revision, nullableProviderTime(p.LastVerifiedAt), stamp(p.UpdatedAt), p.ID, expected, p.Endpoint, p.PrivateNetworkID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrProviderChanged
	}
	return err
}

func scanInfrastructureProvider(row scanner) (core.InfrastructureProvider, error) {
	var p core.InfrastructureProvider
	var network, secret, verified sql.NullString
	var caps, manifest, created, updated string
	err := row.Scan(&p.ID, &p.Name, &p.Endpoint, &network, &secret, &p.Enabled, &caps, &manifest, &p.ManifestDigest, &p.State, &p.LastError, &p.Revision, &verified, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(caps), &p.Capabilities); err != nil {
		return p, err
	}
	p.Manifest = json.RawMessage(manifest)
	p.PrivateNetworkID = network.String
	p.CredentialSecretID = secret.String
	p.CreatedAt = parseTime(created)
	p.UpdatedAt = parseTime(updated)
	if verified.Valid {
		at := parseTime(verified.String)
		p.LastVerifiedAt = &at
	}
	return p, nil
}
func (s *SQLStore) GetInfrastructureProvider(ctx context.Context, id string) (core.InfrastructureProvider, error) {
	return scanInfrastructureProvider(s.db.QueryRowContext(ctx, s.q(`SELECT `+infrastructureProviderColumns+` FROM infrastructure_providers WHERE id=?`), id))
}
func (s *SQLStore) ListInfrastructureProviders(ctx context.Context) ([]core.InfrastructureProvider, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+infrastructureProviderColumns+` FROM infrastructure_providers ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.InfrastructureProvider{}
	for rows.Next() {
		p, err := scanInfrastructureProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func nullableProviderTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return stamp(*value)
}

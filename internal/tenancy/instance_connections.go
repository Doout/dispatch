package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const BackendConnected = "connected"
const BackendEmbedded = "embedded"

type InstanceConnection struct {
	TenantID       string `json:"-"`
	BackendMode    string `json:"backendMode"`
	Generation     int64  `json:"-"`
	CredentialHash string `json:"-"`
	InstallationID string `json:"-"`
	DirectURL      string `json:"instanceUrl"`
	RemoteAccess   bool   `json:"remoteAccess"`
	OwnerSubject   string `json:"-"`
}

const connectionColumns = `tenant_id,mode,generation,credential_hash,installation_id,direct_url,remote_access,owner_subject`

func scanConnection(row scanner) (InstanceConnection, error) {
	var v InstanceConnection
	var remote int
	err := row.Scan(&v.TenantID, &v.BackendMode, &v.Generation, &v.CredentialHash, &v.InstallationID, &v.DirectURL, &remote, &v.OwnerSubject)
	v.RemoteAccess = remote == 1
	return v, missing(err)
}
func (c *Catalog) InstanceConnection(ctx context.Context, id string) (InstanceConnection, error) {
	v, err := scanConnection(c.db.QueryRowContext(ctx, c.q(`SELECT `+connectionColumns+` FROM instance_connections WHERE tenant_id=?`), id))
	if errors.Is(err, ErrNotFound) {
		if _, err = c.Tenant(ctx, id); err == nil {
			return InstanceConnection{TenantID: id, BackendMode: BackendEmbedded}, nil
		}
	}
	return v, err
}
func (c *Catalog) CreateInstanceEnrollment(ctx context.Context, actor, id string) (string, time.Time, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback()
	if err = c.membershipWrite(ctx, tx, actor, id); err != nil {
		return "", time.Time{}, err
	}
	v, err := scanConnection(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+connectionColumns+` FROM instance_connections WHERE tenant_id=?`)), id))
	if err != nil {
		return "", time.Time{}, err
	}
	if v.BackendMode != BackendConnected || v.CredentialHash != "" {
		return "", time.Time{}, ErrConflict
	}
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiry := c.now().Add(10 * time.Minute)
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO instance_enrollments(tenant_id,token_hash,owner_subject,expires_at) VALUES(?,?,?,?) ON CONFLICT(tenant_id) DO UPDATE SET token_hash=excluded.token_hash,owner_subject=excluded.owner_subject,expires_at=excluded.expires_at`), id, hashToken(token), actor, millis(expiry))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiry, tx.Commit()
}
func validInstanceURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && !strings.ContainsAny(raw, "\r\n") && len(raw) <= 2048
}
func (c *Catalog) EnrollInstance(ctx context.Context, token, installation, direct string, remote bool) (InstanceConnection, string, error) {
	if len(token) > 256 || installation == "" || len(installation) > 120 || !validInstanceURL(direct) {
		return InstanceConnection{}, "", ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return InstanceConnection{}, "", err
	}
	defer tx.Rollback()
	if c.postgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7136449553)`); err != nil {
			return InstanceConnection{}, "", err
		}
	}
	var id, owner string
	var expires int64
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT tenant_id,owner_subject,expires_at FROM instance_enrollments WHERE token_hash=?`)), hashToken(token)).Scan(&id, &owner, &expires)
	if err != nil {
		return InstanceConnection{}, "", missing(err)
	}
	if expires <= millis(c.now()) {
		return InstanceConnection{}, "", ErrExpired
	}
	if err = c.membershipWrite(ctx, tx, owner, id); err != nil {
		return InstanceConnection{}, "", err
	}
	v, err := scanConnection(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+connectionColumns+` FROM instance_connections WHERE tenant_id=?`)), id))
	if err != nil {
		return v, "", err
	}
	if v.BackendMode != BackendConnected || v.CredentialHash != "" {
		return v, "", ErrConflict
	}
	credential, err := randomToken()
	if err != nil {
		return v, "", err
	}
	v.Generation++
	v.CredentialHash = hashToken(credential)
	v.InstallationID = installation
	v.DirectURL = direct
	v.RemoteAccess = remote
	v.OwnerSubject = owner
	_, err = tx.ExecContext(ctx, c.q(`UPDATE instance_connections SET generation=?,credential_hash=?,installation_id=?,direct_url=?,remote_access=?,owner_subject=? WHERE tenant_id=?`), v.Generation, v.CredentialHash, installation, direct, boolInt(remote), owner, id)
	if err != nil {
		return v, "", err
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM instance_enrollments WHERE tenant_id=?`), id); err != nil {
		return v, "", err
	}
	if _, err = tx.ExecContext(ctx, c.q(`UPDATE tenants SET state='active' WHERE id=?`), id); err != nil {
		return v, "", err
	}
	return v, credential, tx.Commit()
}
func (c *Catalog) AuthenticateInstance(ctx context.Context, credential string) (InstanceConnection, error) {
	if credential == "" || len(credential) > 256 {
		return InstanceConnection{}, ErrDenied
	}
	v, err := scanConnection(c.db.QueryRowContext(ctx, c.q(`SELECT `+connectionColumns+` FROM instance_connections WHERE credential_hash=? AND mode='connected'`), hashToken(credential)))
	return v, err
}
func (c *Catalog) UpdateInstancePolicy(ctx context.Context, v InstanceConnection, direct string, remote bool) error {
	if !validInstanceURL(direct) {
		return ErrInvalid
	}
	res, err := c.db.ExecContext(ctx, c.q(`UPDATE instance_connections SET direct_url=?,remote_access=? WHERE tenant_id=? AND generation=? AND credential_hash=?`), direct, boolInt(remote), v.TenantID, v.Generation, v.CredentialHash)
	return requireConnectionUpdate(res, err)
}
func requireConnectionUpdate(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrDenied
	}
	return err
}
func (c *Catalog) RevokeInstance(ctx context.Context, actor, id string) (InstanceConnection, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return InstanceConnection{}, err
	}
	defer tx.Rollback()
	if err = c.membershipWrite(ctx, tx, actor, id); err != nil {
		return InstanceConnection{}, err
	}
	v, err := c.revokeInstance(ctx, tx, id, -1)
	if err != nil {
		return v, err
	}
	return v, tx.Commit()
}
func (c *Catalog) RevokeInstanceCredential(ctx context.Context, v InstanceConnection) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.postgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7136449553)`); err != nil {
			return err
		}
	}
	if _, err = c.revokeInstance(ctx, tx, v.TenantID, v.Generation); err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Catalog) revokeInstance(ctx context.Context, tx *sql.Tx, id string, generation int64) (InstanceConnection, error) {
	v, err := scanConnection(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+connectionColumns+` FROM instance_connections WHERE tenant_id=?`)), id))
	if err != nil {
		return v, err
	}
	if v.BackendMode != BackendConnected || generation >= 0 && generation != v.Generation {
		return v, ErrDenied
	}
	_, err = tx.ExecContext(ctx, c.q(`UPDATE instance_connections SET generation=generation+1,credential_hash='',installation_id='',direct_url='',remote_access=0,owner_subject='' WHERE tenant_id=?`), id)
	if err != nil {
		return v, err
	}
	return v, c.revokeInstanceTokens(ctx, tx, id)
}

func (c *Catalog) revokeInstanceTokens(ctx context.Context, tx *sql.Tx, id string) error {
	if _, err := tx.ExecContext(ctx, c.q(`DELETE FROM instance_enrollments WHERE tenant_id=?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, c.q(`DELETE FROM tenant_handoffs WHERE tenant_id=?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, c.q(`DELETE FROM tenancy_login_origins WHERE token_hash IN (SELECT token_hash FROM tenancy_sessions WHERE audience=?)`), TenantAudience(id)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, c.q(`DELETE FROM tenancy_sessions WHERE audience=?`), TenantAudience(id)); err != nil {
		return err
	}
	return nil
}

// ActiveInstanceGrants is one bounded catalog read for the connection watchdog.
func (c *Catalog) ActiveInstanceGrants(ctx context.Context) (map[string]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT c.tenant_id,c.generation,c.credential_hash FROM instance_connections c JOIN tenants t ON t.id=c.tenant_id WHERE c.credential_hash<>'' AND t.state='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, hash string
		var generation int64
		if err = rows.Scan(&id, &generation, &hash); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s:%d", id, generation)] = hash
	}
	return out, rows.Err()
}

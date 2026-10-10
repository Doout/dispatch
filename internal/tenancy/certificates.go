package tenancy

import (
	"context"
	"time"
)

const CertificateTokenLifetime = 90 * 24 * time.Hour

// RotateCertificateToken grants only certificate download access to one tenant.
// Changing the issuer's membership revokes this credential as well.
func (c *Catalog) RotateCertificateToken(ctx context.Context, tenant, user string) (string, time.Time, error) {
	u, version, err := c.audienceUser(ctx, c.db, user, TenantAudience(tenant))
	if err != nil {
		return "", time.Time{}, err
	}
	membership, err := c.Membership(ctx, tenant, u.ID)
	if err != nil || (membership.Role != RoleOwner && membership.Role != RoleAdmin) || membership.Version != version {
		return "", time.Time{}, ErrDenied
	}
	token, err := randomToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := c.now().Add(CertificateTokenLifetime)
	_, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_certificate_tokens(tenant_id,token_hash,user_id,membership_version,expires_at,created_at) VALUES(?,?,?,?,?,?) ON CONFLICT(tenant_id) DO UPDATE SET token_hash=excluded.token_hash,user_id=excluded.user_id,membership_version=excluded.membership_version,expires_at=excluded.expires_at,created_at=excluded.created_at`), tenant, hashToken(token), user, version, millis(expires), millis(c.now()))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (c *Catalog) AuthenticateCertificateToken(ctx context.Context, tenant, token string) error {
	if token == "" || len(token) > 512 {
		return ErrDenied
	}
	var found int
	err := c.db.QueryRowContext(ctx, c.q(`SELECT 1 FROM tenant_certificate_tokens k JOIN tenants t ON t.id=k.tenant_id JOIN tenant_memberships m ON m.tenant_id=k.tenant_id AND m.user_id=k.user_id JOIN tenancy_users u ON u.id=k.user_id WHERE k.tenant_id=? AND k.token_hash=? AND k.expires_at>? AND t.state='active' AND m.state='active' AND m.role IN ('owner','admin') AND m.version=k.membership_version AND u.state='active' AND u.email_verified=1`), tenant, hashToken(token), millis(c.now())).Scan(&found)
	if err != nil {
		return ErrDenied
	}
	return nil
}

func (c *Catalog) RevokeCertificateToken(ctx context.Context, tenant, user string) error {
	if _, _, err := c.audienceUser(ctx, c.db, user, TenantAudience(tenant)); err != nil {
		return err
	}
	membership, err := c.Membership(ctx, tenant, user)
	if err != nil || (membership.Role != RoleOwner && membership.Role != RoleAdmin) {
		return ErrDenied
	}
	_, err = c.db.ExecContext(ctx, c.q(`DELETE FROM tenant_certificate_tokens WHERE tenant_id=?`), tenant)
	return err
}

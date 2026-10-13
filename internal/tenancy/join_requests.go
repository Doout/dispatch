package tenancy

import (
	"context"
	"strings"
)

// A request records intent only. Membership still requires a tenant owner.
func (c *Catalog) RequestTenantJoin(ctx context.Context, user, slug string) error {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if !ValidSlug(slug) {
		return ErrInvalid
	}
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
	u, err := scanUser(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`)), user))
	if err != nil {
		return err
	}
	if u.State != StateActive || !u.EmailVerified {
		return ErrDenied
	}
	tenant, err := scanTenant(tx.QueryRowContext(ctx, c.q(`SELECT id,slug,name,state,created_at FROM tenants WHERE slug=?`), slug))
	if err != nil {
		return err
	}
	if tenant.State != StateActive {
		return ErrDenied
	}
	var count int
	if err = tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*) FROM tenant_memberships WHERE tenant_id=? AND user_id=?`), tenant.ID, user).Scan(&count); err != nil {
		return err
	}
	// Disabled memberships are deliberate removals, not new requests.
	if count != 0 {
		return ErrDenied
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_join_requests(tenant_id,user_id,created_at) VALUES(?,?,?) ON CONFLICT(tenant_id,user_id) DO NOTHING`), tenant.ID, user, millis(c.now()))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Catalog) PendingJoinRequests(ctx context.Context, user string) ([]Tenant, error) {
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT t.id,t.slug,t.name,t.state,t.created_at FROM tenant_join_requests r JOIN tenants t ON t.id=r.tenant_id WHERE r.user_id=? AND NOT EXISTS (SELECT 1 FROM tenant_memberships m WHERE m.tenant_id=r.tenant_id AND m.user_id=r.user_id) ORDER BY r.created_at,t.id`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Tenant{}
	for rows.Next() {
		tenant, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, tenant)
	}
	return result, rows.Err()
}

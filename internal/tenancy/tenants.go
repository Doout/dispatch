package tenancy

import (
	"context"
	"database/sql"
	"regexp"
	"sort"
	"strings"

	"github.com/oklog/ulid/v2"
)

var previewWorkloadPrefix = regexp.MustCompile(`^p[0-9]+-`)

var tenantSlug = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var reservedSlugs = map[string]bool{"local": true, "www": true, "api": true, "admin": true, "auth": true, "login": true, "platform": true, "settings": true, "assets": true, "static": true, "healthz": true, "mail": true, "smtp": true, "ns": true, "ns1": true, "ns2": true, "_acme-challenge": true}

func ReservedTenantPrefix(slug string) bool {
	return previewWorkloadPrefix.MatchString(slug) || strings.HasPrefix(slug, "pr-") || strings.HasPrefix(slug, "app-")
}
func ValidSlug(slug string) bool {
	return tenantSlug.MatchString(slug) && !reservedSlugs[slug] && !ReservedTenantPrefix(slug)
}

func ReservedTenantNames() []string {
	names := make([]string, 0, len(reservedSlugs))
	for name := range reservedSlugs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Catalog) CreateTenant(ctx context.Context, in CreateTenantInput) (Tenant, error) {
	return c.createTenant(ctx, in, false)
}

func (c *Catalog) CreateOwnConnectedTenant(ctx context.Context, user, slug, name string) (Tenant, error) {
	return c.createTenant(ctx, CreateTenantInput{CreatorID: user, InitialOwnerID: user, Slug: slug, Name: name, BackendMode: "connected"}, true)
}

// CreateOwnTenant assigns the caller as owner without granting platform access.
func (c *Catalog) CreateOwnTenant(ctx context.Context, user, slug, name string) (Tenant, error) {
	return c.createTenant(ctx, CreateTenantInput{CreatorID: user, InitialOwnerID: user, Slug: slug, Name: name}, true)
}

func (c *Catalog) createTenant(ctx context.Context, in CreateTenantInput, own bool) (Tenant, error) {
	if in.BackendMode == "" {
		in.BackendMode = "embedded"
	}
	if in.BackendMode != "embedded" && in.BackendMode != "connected" {
		return Tenant{}, ErrInvalid
	}
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Name = strings.TrimSpace(in.Name)
	in.InvitationEmail = normalizeEmail(in.InvitationEmail)
	if !ValidSlug(in.Slug) || in.Name == "" || len(in.Name) > 120 || (in.InitialOwnerID == "") == (in.InvitationEmail == "") {
		return Tenant{}, ErrInvalid
	}
	if in.InvitationEmail != "" && !validEmail(in.InvitationEmail) {
		return Tenant{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Tenant{}, err
	}
	defer tx.Rollback()
	if c.postgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7136449553)`); err != nil {
			return Tenant{}, err
		}
	}
	creator, err := scanUser(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`)), in.CreatorID))
	if err != nil {
		return Tenant{}, err
	}
	if creator.State != StateActive || (!own && !creator.PlatformAdmin) || (own && (!creator.EmailVerified || in.InitialOwnerID != creator.ID || in.InvitationEmail != "")) {
		return Tenant{}, ErrDenied
	}
	var reserved, owned int
	if err = tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN user_id=? THEN 1 ELSE 0 END),0) FROM instance_name_reservations WHERE slug=?`), in.InitialOwnerID, in.Slug).Scan(&reserved, &owned); err != nil {
		return Tenant{}, err
	}
	if reserved > 0 && owned == 0 {
		return Tenant{}, ErrConflict
	}
	if in.InitialOwnerID != "" {
		owner, err := scanUser(tx.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`), in.InitialOwnerID))
		if err != nil {
			return Tenant{}, err
		}
		if owner.State != StateActive || !owner.EmailVerified {
			return Tenant{}, ErrDenied
		}
	}
	now := c.now().UTC()
	tenant := Tenant{ID: ulid.Make().String(), Slug: in.Slug, Name: in.Name, State: "pending", CreatedAt: now}
	if in.BackendMode == BackendConnected {
		tenant.State = StateActive
	}
	if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenants(id,slug,name,state,created_at) VALUES(?,?,?,?,?)`), tenant.ID, tenant.Slug, tenant.Name, tenant.State, millis(now)); err != nil {
		return Tenant{}, conflict(err)
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM instance_name_reservations WHERE slug=?`), tenant.Slug); err != nil {
		return Tenant{}, err
	}
	if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO instance_connections(tenant_id,mode) VALUES(?,?)`), tenant.ID, in.BackendMode); err != nil {
		return Tenant{}, err
	}
	if in.InitialOwnerID != "" {
		_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_memberships(tenant_id,user_id,role,state,version,created_at) VALUES(?,?,'owner','active',1,?)`), tenant.ID, in.InitialOwnerID, millis(now))
	} else {
		_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_invitations(tenant_id,email,created_at) VALUES(?,?,?)`), tenant.ID, in.InvitationEmail, millis(now))
	}
	if err != nil {
		return Tenant{}, err
	}
	jobState, phase := "pending", "database"
	if in.BackendMode == BackendConnected {
		jobState, phase = "succeeded", "awaiting connection"
	}
	if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_provisioning(tenant_id,state,phase,updated_at) VALUES(?,?,?,?)`), tenant.ID, jobState, phase, millis(now)); err != nil {
		return Tenant{}, err
	}
	if err = tx.Commit(); err != nil {
		return Tenant{}, err
	}
	return tenant, nil
}

func scanTenant(row scanner) (Tenant, error) {
	var t Tenant
	var created int64
	err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.State, &created)
	t.CreatedAt = instant(created)
	return t, missing(err)
}
func (c *Catalog) Tenant(ctx context.Context, id string) (Tenant, error) {
	return scanTenant(c.db.QueryRowContext(ctx, c.q(`SELECT id,slug,name,state,created_at FROM tenants WHERE id=?`), id))
}
func (c *Catalog) TenantBySlug(ctx context.Context, slug string) (Tenant, error) {
	return scanTenant(c.db.QueryRowContext(ctx, c.q(`SELECT id,slug,name,state,created_at FROM tenants WHERE slug=?`), strings.ToLower(slug)))
}
func (c *Catalog) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id,slug,name,state,created_at FROM tenants ORDER BY slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetTenantState is an internal provisioning transition. It is deliberately not
// an administrative capability and must not be exposed as a platform API.
func (c *Catalog) SetTenantState(ctx context.Context, id, state string) error {
	if state != "pending" && state != "provisioning" && state != "active" && state != "failed" {
		return ErrInvalid
	}
	res, err := c.db.ExecContext(ctx, c.q(`UPDATE tenants SET state=? WHERE id=?`), state, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMembership(row scanner) (Membership, error) {
	var m Membership
	var created int64
	err := row.Scan(&m.TenantID, &m.UserID, &m.Role, &m.State, &m.Version, &created)
	m.CreatedAt = instant(created)
	return m, missing(err)
}

const membershipColumns = `tenant_id,user_id,role,state,version,created_at`

func (c *Catalog) Membership(ctx context.Context, tenant, user string) (Membership, error) {
	m, err := scanMembership(c.db.QueryRowContext(ctx, c.q(`SELECT `+membershipColumns+` FROM tenant_memberships WHERE tenant_id=? AND user_id=?`), tenant, user))
	if err != nil {
		return m, err
	}
	u, err := c.User(ctx, user)
	if err != nil {
		return Membership{}, err
	}
	if u.State != StateActive || m.State != StateActive {
		return Membership{}, ErrDenied
	}
	return m, nil
}
func (c *Catalog) ListMemberships(ctx context.Context, user string) ([]Membership, error) {
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT m.tenant_id,m.user_id,m.role,m.state,m.version,m.created_at FROM tenant_memberships m JOIN tenancy_users u ON u.id=m.user_id WHERE m.user_id=? AND m.state='active' AND u.state='active' ORDER BY m.tenant_id`), user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (c *Catalog) TenantMembers(ctx context.Context, tenant string) ([]Membership, error) {
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT `+membershipColumns+` FROM tenant_memberships WHERE tenant_id=? ORDER BY user_id`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		m, err := scanMembership(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type MemberUser struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}
type TenantMember struct {
	Membership
	User MemberUser `json:"user"`
}

func (c *Catalog) ListTenantMembers(ctx context.Context, actor, tenant string) ([]TenantMember, error) {
	m, err := c.Membership(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	if m.Role != RoleOwner && m.Role != RoleAdmin {
		return nil, ErrDenied
	}
	items, err := c.TenantMembers(ctx, tenant)
	if err != nil {
		return nil, err
	}
	out := []TenantMember{}
	for _, member := range items {
		u, err := c.User(ctx, member.UserID)
		if err != nil {
			return nil, err
		}
		out = append(out, TenantMember{Membership: member, User: MemberUser{ID: u.ID, Name: u.Name, Email: u.Email}})
	}
	return out, nil
}

func (c *Catalog) membershipWrite(ctx context.Context, tx *sql.Tx, actor, tenant string) error {
	if c.postgres {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7136449553)`); err != nil {
			return err
		}
	}
	var count int
	err := tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*) FROM tenant_memberships m JOIN tenancy_users u ON u.id=m.user_id WHERE m.tenant_id=? AND m.user_id=? AND m.role='owner' AND m.state='active' AND u.state='active'`), tenant, actor).Scan(&count)
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrDenied
	}
	return nil
}

func (c *Catalog) SetMembership(ctx context.Context, actor string, m Membership) error {
	if !IsTenantRole(m.Role) {
		return ErrInvalid
	}
	if m.State == "" {
		m.State = StateActive
	}
	if m.State != StateActive && m.State != StateDisabled {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = c.membershipWrite(ctx, tx, actor, m.TenantID); err != nil {
		return err
	}
	u, err := scanUser(tx.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`), m.UserID))
	if err != nil {
		return err
	}
	if u.State != StateActive || !u.EmailVerified {
		return ErrDenied
	}
	if m.Role != RoleOwner || m.State != StateActive {
		if err = c.checkOtherOwner(ctx, tx, m.TenantID, m.UserID); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_memberships(tenant_id,user_id,role,state,version,created_at) VALUES(?,?,?,?,1,?) ON CONFLICT(tenant_id,user_id) DO UPDATE SET role=excluded.role,state=excluded.state,version=tenant_memberships.version+1`), m.TenantID, m.UserID, m.Role, m.State, millis(c.now()))
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenant_join_requests WHERE tenant_id=? AND user_id=?`), m.TenantID, m.UserID); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Catalog) checkOtherOwner(ctx context.Context, tx *sql.Tx, tenant, user string) error {
	var sole int
	err := tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*) FROM tenant_memberships m WHERE m.tenant_id=? AND m.user_id=? AND m.role='owner' AND m.state='active' AND NOT EXISTS (SELECT 1 FROM tenant_memberships other JOIN tenancy_users u ON u.id=other.user_id WHERE other.tenant_id=m.tenant_id AND other.user_id<>m.user_id AND other.role='owner' AND other.state='active' AND u.state='active')`), tenant, user).Scan(&sole)
	if err != nil {
		return err
	}
	if sole > 0 {
		return ErrLastOwner
	}
	return nil
}
func (c *Catalog) DeleteMembership(ctx context.Context, actor, tenant, user string) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = c.membershipWrite(ctx, tx, actor, tenant); err != nil {
		return err
	}
	if err = c.checkOtherOwner(ctx, tx, tenant, user); err != nil {
		return err
	}
	// Keep the row and advance its version so old sessions stay revoked even if
	// this identity is invited back to the same tenant later.
	res, err := tx.ExecContext(ctx, c.q(`UPDATE tenant_memberships SET state='disabled',version=version+1 WHERE tenant_id=? AND user_id=?`), tenant, user)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

func (c *Catalog) AcceptInvitation(ctx context.Context, user, tenant string) error {
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
	var email string
	var accepted sql.NullString
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT email,accepted_by FROM tenant_invitations WHERE tenant_id=?`)), tenant).Scan(&email, &accepted)
	if err != nil {
		return missing(err)
	}
	if accepted.Valid {
		return ErrDenied
	}
	u, err := scanUser(tx.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`), user))
	if err != nil {
		return err
	}
	if u.State != StateActive || !u.EmailVerified || u.Email != email {
		return ErrDenied
	}
	if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_memberships(tenant_id,user_id,role,state,version,created_at) VALUES(?,?,'owner','active',1,?)`), tenant, user, millis(c.now())); err != nil {
		return conflict(err)
	}
	if _, err = tx.ExecContext(ctx, c.q(`UPDATE tenant_invitations SET accepted_by=? WHERE tenant_id=?`), user, tenant); err != nil {
		return err
	}
	return tx.Commit()
}

// PendingInvitations discloses a handoff only to the verified intended owner.
// Platform administrators do not get an invitation-discovery bypass.
func (c *Catalog) PendingInvitations(ctx context.Context, user string) ([]Tenant, error) {
	u, err := c.User(ctx, user)
	if err != nil {
		return nil, err
	}
	if u.State != StateActive || !u.EmailVerified {
		return nil, ErrDenied
	}
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT t.id,t.slug,t.name,t.state,t.created_at FROM tenants t JOIN tenant_invitations i ON i.tenant_id=t.id WHERE i.email=? AND i.accepted_by IS NULL ORDER BY t.slug`), u.Email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Tenant{}
	for rows.Next() {
		item, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

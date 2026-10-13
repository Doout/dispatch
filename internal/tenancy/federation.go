package tenancy

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"
)

// ExternalUser binds an immutable provider subject to a Dispatch identity.
// Matching email addresses never establish this binding.
func (c *Catalog) ExternalUser(ctx context.Context, provider, subject string) (User, error) {
	var id string
	err := c.db.QueryRowContext(ctx, c.q(`SELECT user_id FROM tenancy_external_users WHERE provider=? AND subject=?`), provider, subject).Scan(&id)
	if err != nil {
		return User{}, missing(err)
	}
	return c.User(ctx, id)
}

func (c *Catalog) ExternalSubject(ctx context.Context, provider, user string) (string, error) {
	var subject string
	err := c.db.QueryRowContext(ctx, c.q(`SELECT subject FROM tenancy_external_users WHERE provider=? AND user_id=?`), provider, user).Scan(&subject)
	return subject, missing(err)
}

type ExternalLogin struct {
	Provider    string
	Subject     string
	Email       string
	Name        string
	LinkUserID  string
	LinkVersion int64
}

func (c *Catalog) ResolveExternalUser(ctx context.Context, login ExternalLogin) (User, error) {
	provider, subject, email, name, linkUser := login.Provider, login.Subject, login.Email, login.Name, login.LinkUserID
	if provider == "" || subject == "" || len(subject) > 256 {
		return User{}, ErrInvalid
	}
	u, err := c.ExternalUser(ctx, provider, subject)
	if err == nil {
		if u.State != StateActive || (linkUser != "" && (linkUser != u.ID || u.Version != login.LinkVersion)) {
			return User{}, ErrDenied
		}
		return u, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return User{}, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	if linkUser != "" {
		u, err = scanUser(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`)), linkUser))
		if err != nil {
			return User{}, err
		}
		if u.State != StateActive || !u.EmailVerified || u.Version != login.LinkVersion {
			return User{}, ErrDenied
		}
	} else {
		u = User{Email: email, Name: name, EmailVerified: true}
		if err = prepareUser(&u, c.now()); err != nil {
			return User{}, err
		}
		_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenancy_users(`+userColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`), u.ID, u.Email, u.Name, u.State, 0, 1, "", u.Version, millis(u.CreatedAt))
		if err != nil {
			return User{}, conflict(err)
		}
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenancy_external_users(provider,subject,user_id) VALUES(?,?,?)`), provider, subject, u.ID)
	if err != nil {
		return User{}, conflict(err)
	}
	return u, tx.Commit()
}

type LoginAttempt struct {
	Verifier        string
	ReturnTo        string
	LinkUser        string
	LinkVersion     int64
	OrganizationID  string
	InvitationToken string          `json:",omitempty"`
	Continuation    json.RawMessage `json:",omitempty"`
}

func (c *Catalog) CreateLoginAttempt(ctx context.Context, browser string, attempt LoginAttempt) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(attempt)
	if err != nil {
		return "", err
	}
	_, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_login_attempts(token_hash,browser_hash,data,expires_at) VALUES(?,?,?,?)`), hashToken(token), hashToken(browser), string(raw), millis(c.now().Add(10*time.Minute)))
	return token, err
}

func (c *Catalog) ConsumeLoginAttempt(ctx context.Context, token, browser string) (LoginAttempt, error) {
	if token == "" || browser == "" || len(token) > 512 || len(browser) > 512 {
		return LoginAttempt{}, ErrExpired
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return LoginAttempt{}, err
	}
	defer tx.Rollback()
	var saved, raw string
	var expires int64
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT browser_hash,data,expires_at FROM tenancy_login_attempts WHERE token_hash=?`)), hashToken(token)).Scan(&saved, &raw, &expires)
	if err != nil {
		return LoginAttempt{}, missing(err)
	}
	if expires <= millis(c.now()) || subtle.ConstantTimeCompare([]byte(saved), []byte(hashToken(browser))) != 1 {
		return LoginAttempt{}, ErrExpired
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenancy_login_attempts WHERE token_hash=?`), hashToken(token)); err != nil {
		return LoginAttempt{}, err
	}
	var attempt LoginAttempt
	if err = json.Unmarshal([]byte(raw), &attempt); err != nil {
		return LoginAttempt{}, err
	}
	if attempt.LinkUser != "" {
		u, e := scanUser(tx.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`), attempt.LinkUser))
		if e != nil || u.State != StateActive || u.Version != attempt.LinkVersion {
			return LoginAttempt{}, ErrDenied
		}
	}
	return attempt, tx.Commit()
}

type LoginOrigin struct{ Provider, Subject, SessionID string }

func (c *Catalog) LoginOrigin(ctx context.Context, token string) (LoginOrigin, error) {
	var o LoginOrigin
	err := c.db.QueryRowContext(ctx, c.q(`SELECT provider,subject,session_id FROM tenancy_login_origins WHERE token_hash=? AND expires_at>?`), hashToken(token), millis(c.now())).Scan(&o.Provider, &o.Subject, &o.SessionID)
	return o, missing(err)
}

func (c *Catalog) CreateExternalSession(ctx context.Context, user, audience string, ttl time.Duration, o LoginOrigin) (string, error) {
	if o.Provider == "" || o.Subject == "" {
		return "", ErrInvalid
	}
	token, err := c.CreateSession(ctx, user, audience, ttl)
	if err != nil {
		return "", err
	}
	_, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_login_origins(token_hash,provider,subject,session_id,expires_at) VALUES(?,?,?,?,?)`), hashToken(token), o.Provider, o.Subject, o.SessionID, millis(c.now().Add(ttl)))
	if err != nil {
		_ = c.RevokeSession(ctx, token)
		return "", err
	}
	return token, nil
}

func (c *Catalog) CopyLoginOrigin(ctx context.Context, from, to string, ttl time.Duration) error {
	_, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_login_origins(token_hash,provider,subject,session_id,expires_at) SELECT ?,provider,subject,session_id,? FROM tenancy_login_origins WHERE token_hash=? AND expires_at>?`), hashToken(to), millis(c.now().Add(ttl)), hashToken(from), millis(c.now()))
	return err
}

func (c *Catalog) WorkOSOrganization(ctx context.Context, tenant string) (string, error) {
	var id string
	err := c.db.QueryRowContext(ctx, c.q(`SELECT organization_id FROM tenant_workos_organizations WHERE tenant_id=?`), tenant).Scan(&id)
	return id, missing(err)
}
func (c *Catalog) BindWorkOSOrganization(ctx context.Context, tenant, id string) error {
	if id == "" {
		return ErrInvalid
	}
	_, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_workos_organizations(tenant_id,organization_id) VALUES(?,?) ON CONFLICT(tenant_id) DO NOTHING`), tenant, id)
	return conflict(err)
}

func (c *Catalog) WorkOSSyncDigest(ctx context.Context, tenant string) (string, error) {
	var digest string
	err := c.db.QueryRowContext(ctx, c.q(`SELECT sync_digest FROM tenant_workos_organizations WHERE tenant_id=?`), tenant).Scan(&digest)
	return digest, missing(err)
}
func (c *Catalog) SaveWorkOSSyncDigest(ctx context.Context, tenant, digest string) error {
	_, err := c.db.ExecContext(ctx, c.q(`UPDATE tenant_workos_organizations SET sync_digest=? WHERE tenant_id=?`), digest, tenant)
	return err
}

// TenantJoinRequests includes explicit requests and tenant provider identities
// awaiting owner approval. Removed members do not reappear as new requests.
func (c *Catalog) TenantJoinRequests(ctx context.Context, actor, tenant string) ([]MemberUser, error) {
	m, err := c.Membership(ctx, tenant, actor)
	if err != nil {
		return nil, err
	}
	if m.Role != RoleOwner {
		return nil, ErrDenied
	}
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT u.id,u.name,u.email FROM tenancy_users u WHERE u.state='active' AND u.email_verified=1 AND (EXISTS (SELECT 1 FROM tenancy_external_users e WHERE e.user_id=u.id AND e.provider LIKE ?) OR EXISTS (SELECT 1 FROM tenant_join_requests r WHERE r.user_id=u.id AND r.tenant_id=?)) AND NOT EXISTS (SELECT 1 FROM tenant_memberships m WHERE m.tenant_id=? AND m.user_id=u.id) ORDER BY u.email LIMIT 100`), "tenant:"+tenant+":%", tenant, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []MemberUser{}
	for rows.Next() {
		var u MemberUser
		if err = rows.Scan(&u.ID, &u.Name, &u.Email); err != nil {
			return nil, err
		}
		result = append(result, u)
	}
	return result, rows.Err()
}

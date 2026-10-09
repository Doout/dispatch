package tenancy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"
	"time"
)

const SessionLifetime = 12 * time.Hour

func randomToken() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value[:]), nil
}
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (c *Catalog) audienceUser(ctx context.Context, db queryer, user, audience string) (User, int64, error) {
	u, err := scanUser(db.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`), user))
	if err != nil {
		return User{}, 0, err
	}
	if u.State != StateActive {
		return User{}, 0, ErrDenied
	}
	if audience == AudiencePlatform {
		return u, 0, nil
	}
	tenant, ok := strings.CutPrefix(audience, "tenant:")
	if !ok || tenant == "" || !u.EmailVerified {
		return User{}, 0, ErrDenied
	}
	var version int64
	err = db.QueryRowContext(ctx, c.q(`SELECT m.version FROM tenant_memberships m JOIN tenants t ON t.id=m.tenant_id WHERE m.tenant_id=? AND m.user_id=? AND m.state='active' AND t.state='active'`), tenant, user).Scan(&version)
	if err == sql.ErrNoRows {
		return User{}, 0, ErrDenied
	}
	return u, version, err
}

func (c *Catalog) CreateSession(ctx context.Context, user, audience string, ttl time.Duration) (string, error) {
	if ttl <= 0 || ttl > SessionLifetime {
		return "", ErrInvalid
	}
	u, mversion, err := c.audienceUser(ctx, c.db, user, audience)
	if err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_sessions(token_hash,user_id,audience,user_version,membership_version,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`), hashToken(token), user, audience, u.Version, mversion, millis(c.now().Add(ttl)), millis(c.now()))
	if err != nil {
		return "", err
	}
	return token, nil
}

func (c *Catalog) AuthenticateSession(ctx context.Context, token, audience string) (User, error) {
	if token == "" || len(token) > 512 {
		return User{}, ErrExpired
	}
	var id, savedAudience string
	var uv, mv, expires int64
	err := c.db.QueryRowContext(ctx, c.q(`SELECT user_id,audience,user_version,membership_version,expires_at FROM tenancy_sessions WHERE token_hash=?`), hashToken(token)).Scan(&id, &savedAudience, &uv, &mv, &expires)
	if err == sql.ErrNoRows {
		return User{}, ErrExpired
	}
	if err != nil {
		return User{}, err
	}
	if expires <= millis(c.now()) || savedAudience != audience {
		return User{}, ErrExpired
	}
	u, currentMV, err := c.audienceUser(ctx, c.db, id, audience)
	if err != nil || u.Version != uv || currentMV != mv {
		return User{}, ErrExpired
	}
	return u, nil
}
func (c *Catalog) RevokeSession(ctx context.Context, token string) error {
	_, err := c.db.ExecContext(ctx, c.q(`DELETE FROM tenancy_sessions WHERE token_hash=?`), hashToken(token))
	return err
}
func (c *Catalog) RevokeUserSessions(ctx context.Context, user string) error {
	_, err := c.db.ExecContext(ctx, c.q(`UPDATE tenancy_users SET version=version+1 WHERE id=?`), user)
	return err
}

type HandoffRequest struct{ UserID, TenantID, Origin, CodeChallenge string }
type HandoffExchange struct {
	Code, TenantID, Origin, CodeVerifier string
	SessionDuration                      time.Duration
}

func validOrigin(origin string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" && u.Host == strings.ToLower(u.Host)
}
func validChallenge(challenge string) bool {
	raw, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(raw) == 32 && base64.RawURLEncoding.EncodeToString(raw) == challenge
}
func validVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	return true
}

func (c *Catalog) CreateHandoff(ctx context.Context, in HandoffRequest) (string, error) {
	if !validOrigin(in.Origin) || !validChallenge(in.CodeChallenge) {
		return "", ErrInvalid
	}
	u, mv, err := c.audienceUser(ctx, c.db, in.UserID, TenantAudience(in.TenantID))
	if err != nil {
		return "", err
	}
	code, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_handoffs(code_hash,user_id,tenant_id,origin,code_challenge,user_version,membership_version,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?)`), hashToken(code), u.ID, in.TenantID, in.Origin, in.CodeChallenge, u.Version, mv, millis(c.now().Add(time.Minute)), millis(c.now()))
	if err != nil {
		return "", err
	}
	return code, nil
}

func (c *Catalog) ExchangeHandoff(ctx context.Context, in HandoffExchange) (string, error) {
	if in.SessionDuration == 0 {
		in.SessionDuration = SessionLifetime
	}
	if in.SessionDuration <= 0 || in.SessionDuration > SessionLifetime {
		return "", ErrInvalid
	}
	if len(in.Code) > 512 || !validOrigin(in.Origin) || !validVerifier(in.CodeVerifier) {
		return "", ErrExpired
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var user, tenant, origin, challenge string
	var uv, mv, expires int64
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT user_id,tenant_id,origin,code_challenge,user_version,membership_version,expires_at FROM tenant_handoffs WHERE code_hash=?`)), hashToken(in.Code)).Scan(&user, &tenant, &origin, &challenge, &uv, &mv, &expires)
	if err == sql.ErrNoRows {
		return "", ErrExpired
	}
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(in.CodeVerifier))
	actualChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if expires <= millis(c.now()) || tenant != in.TenantID || origin != in.Origin || subtle.ConstantTimeCompare([]byte(actualChallenge), []byte(challenge)) != 1 {
		return "", ErrExpired
	}
	u, currentMV, err := c.audienceUser(ctx, tx, user, TenantAudience(tenant))
	if err != nil || u.Version != uv || currentMV != mv {
		return "", ErrExpired
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenant_handoffs WHERE code_hash=?`), hashToken(in.Code)); err != nil {
		return "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenancy_sessions(token_hash,user_id,audience,user_version,membership_version,expires_at,created_at) VALUES(?,?,?,?,?,?,?)`), hashToken(token), user, TenantAudience(tenant), uv, mv, millis(c.now().Add(in.SessionDuration)), millis(c.now()))
	if err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (c *Catalog) PruneCredentials(ctx context.Context) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"tenancy_sessions", "tenant_handoffs", "tenancy_email_verifications"} {
		if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM `+table+` WHERE expires_at<=?`), millis(c.now())); err != nil {
			return err
		}
	}
	return tx.Commit()
}

package tenancy

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
)

const userColumns = `id,email,name,state,platform_admin,email_verified,password_hash,version,created_at`

func scanUser(row scanner) (User, error) {
	var u User
	var admin, verified int
	var created int64
	err := row.Scan(&u.ID, &u.Email, &u.Name, &u.State, &admin, &verified, &u.PasswordHash, &u.Version, &created)
	u.PlatformAdmin, u.EmailVerified, u.CreatedAt = admin != 0, verified != 0, instant(created)
	return u, missing(err)
}

func validEmail(email string) bool {
	return len(email) <= 254 && strings.Count(email, "@") == 1 && !strings.ContainsAny(email, " \r\n\t") && !strings.HasPrefix(email, "@") && !strings.HasSuffix(email, "@")
}

func prepareUser(u *User, now time.Time) error {
	u.Email = normalizeEmail(u.Email)
	u.Name = strings.TrimSpace(u.Name)
	if !validEmail(u.Email) || u.Name == "" || len(u.Name) > 120 {
		return ErrInvalid
	}
	if u.ID == "" {
		u.ID = ulid.Make().String()
	}
	if u.State == "" {
		u.State = StateActive
	}
	if u.State != StateActive && u.State != StateDisabled && u.State != StatePending {
		return ErrInvalid
	}
	u.Version = 1
	u.CreatedAt = now.UTC()
	return nil
}

func (c *Catalog) CreateUser(ctx context.Context, u User) error {
	if err := prepareUser(&u, c.now()); err != nil {
		return err
	}
	_, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_users(`+userColumns+`) VALUES(?,?,?,?,?,?,?,?,?)`), u.ID, u.Email, u.Name, u.State, boolInt(u.PlatformAdmin), boolInt(u.EmailVerified), u.PasswordHash, u.Version, millis(u.CreatedAt))
	return conflict(err)
}

// BootstrapUser is only for the configured first platform administrator. It
// cannot elevate an existing account or create another administrator later.
func (c *Catalog) BootstrapUser(ctx context.Context, u User) error {
	u.PlatformAdmin, u.EmailVerified, u.State = true, true, StateActive
	if err := prepareUser(&u, c.now()); err != nil {
		return err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.postgres {
		if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(7136449552)`); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, c.q(`INSERT INTO tenancy_users(`+userColumns+`) SELECT ?,?,?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM tenancy_users)`), u.ID, u.Email, u.Name, u.State, 1, 1, u.PasswordHash, u.Version, millis(u.CreatedAt))
	if err != nil {
		return conflict(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return tx.Commit()
}

func (c *Catalog) User(ctx context.Context, id string) (User, error) {
	return scanUser(c.db.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`), id))
}
func (c *Catalog) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(c.db.QueryRowContext(ctx, c.q(`SELECT `+userColumns+` FROM tenancy_users WHERE email=?`), normalizeEmail(email)))
}

func (c *Catalog) AuthenticatePassword(ctx context.Context, email, password string) (User, error) {
	u, err := c.UserByEmail(ctx, email)
	if err != nil {
		_ = bcrypt.CompareHashAndPassword(dummyPasswordHash, []byte(password))
		return User{}, ErrDenied
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil || u.State != StateActive {
		return User{}, ErrDenied
	}
	return u, nil
}

// The fixed hash makes unknown-account checks use the same bcrypt work factor.
var dummyPasswordHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

func (c *Catalog) UpdateOwnPassword(ctx context.Context, id, oldPassword, newPassword string) error {
	if len(newPassword) < 12 || len(newPassword) > 72 {
		return ErrInvalid
	}
	u, err := c.User(ctx, id)
	if err != nil {
		return err
	}
	if u.State != StateActive || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(oldPassword)) != nil {
		return ErrDenied
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := c.db.ExecContext(ctx, c.q(`UPDATE tenancy_users SET password_hash=?,version=version+1 WHERE id=? AND version=? AND state='active'`), string(hash), id, u.Version)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrDenied
	}
	return nil
}

func (c *Catalog) SetUserState(ctx context.Context, id, state string) error {
	if state != StateActive && state != StateDisabled && state != StatePending {
		return ErrInvalid
	}
	// User disabling is an identity operation, not a platform HTTP capability.
	// Reject disabling the sole active owner of any tenant.
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
	if state != StateActive {
		var count int
		err = tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*) FROM tenant_memberships m WHERE m.user_id=? AND m.role='owner' AND m.state='active' AND NOT EXISTS (SELECT 1 FROM tenant_memberships other JOIN tenancy_users u ON u.id=other.user_id WHERE other.tenant_id=m.tenant_id AND other.user_id<>m.user_id AND other.role='owner' AND other.state='active' AND u.state='active')`), id).Scan(&count)
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrLastOwner
		}
	}
	res, err := tx.ExecContext(ctx, c.q(`UPDATE tenancy_users SET state=?,version=version+1 WHERE id=?`), state, id)
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

func (c *Catalog) SetPlatformAdmin(ctx context.Context, id string, enabled bool) error {
	result, err := c.db.ExecContext(ctx, c.q(`UPDATE tenancy_users SET platform_admin=?,version=version+1 WHERE id=?`), boolInt(enabled), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (c *Catalog) CreateEmailVerification(ctx context.Context, id string) (string, error) {
	u, err := c.User(ctx, id)
	if err != nil {
		return "", err
	}
	if (u.State != StateActive && u.State != StatePending) || u.EmailVerified {
		return "", ErrDenied
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	_, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_email_verifications(token_hash,user_id,email,expires_at) VALUES(?,?,?,?)`), hashToken(token), id, u.Email, millis(c.now().Add(30*time.Minute)))
	return token, err
}

// CompleteRegistration lets the verified recipient choose the first password.
// A password supplied before email verification must never establish ownership.
func (c *Catalog) CompleteRegistration(ctx context.Context, token, name, password string) (User, error) {
	name = strings.TrimSpace(name)
	if len(token) > 512 || len(password) < 12 || len(password) > 72 || len(name) > 120 {
		return User{}, ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var id, email string
	var expires int64
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT user_id,email,expires_at FROM tenancy_email_verifications WHERE token_hash=?`)), hashToken(token)).Scan(&id, &email, &expires)
	if err == sql.ErrNoRows {
		return User{}, ErrExpired
	}
	if err != nil {
		return User{}, err
	}
	if expires <= millis(c.now()) {
		return User{}, ErrExpired
	}
	u, err := scanUser(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`)), id))
	if err != nil {
		return User{}, err
	}
	if u.State != StatePending || u.EmailVerified || u.PasswordHash != "" || u.Email != email {
		return User{}, ErrDenied
	}
	if name == "" {
		name = u.Name
	}
	if _, err = tx.ExecContext(ctx, c.q(`UPDATE tenancy_users SET name=?,state='active',email_verified=1,password_hash=?,version=version+1 WHERE id=?`), name, string(hash), id); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenancy_email_verifications WHERE user_id=?`), id); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	u.Name, u.State, u.EmailVerified, u.PasswordHash = name, StateActive, true, string(hash)
	u.Version++
	return u, nil
}

func (c *Catalog) VerifyEmail(ctx context.Context, token string) (User, error) {
	if len(token) > 512 {
		return User{}, ErrExpired
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()
	var id, email string
	var expires int64
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT user_id,email,expires_at FROM tenancy_email_verifications WHERE token_hash=?`)), hashToken(token)).Scan(&id, &email, &expires)
	if err == sql.ErrNoRows {
		return User{}, ErrExpired
	}
	if err != nil {
		return User{}, err
	}
	if expires <= millis(c.now()) {
		return User{}, ErrExpired
	}
	u, err := scanUser(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+userColumns+` FROM tenancy_users WHERE id=?`)), id))
	if err != nil {
		return User{}, err
	}
	if u.State != StateActive || subtle.ConstantTimeCompare([]byte(email), []byte(u.Email)) != 1 {
		return User{}, ErrExpired
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenancy_email_verifications WHERE user_id=?`), id); err != nil {
		return User{}, err
	}
	if _, err = tx.ExecContext(ctx, c.q(`UPDATE tenancy_users SET email_verified=1 WHERE id=?`), id); err != nil {
		return User{}, err
	}
	if err = tx.Commit(); err != nil {
		return User{}, err
	}
	u.EmailVerified = true
	return u, nil
}

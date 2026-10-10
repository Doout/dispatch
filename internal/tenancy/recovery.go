package tenancy

import (
	"context"

	"golang.org/x/crypto/bcrypt"
)

// RecoverPlatformAdminPassword is an offline operator action. It cannot create,
// enable or promote an account and never modifies tenant membership.
func (c *Catalog) RecoverPlatformAdminPassword(ctx context.Context, email, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return ErrInvalid
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := c.db.ExecContext(ctx, c.q(`UPDATE tenancy_users SET password_hash=?,version=version+1 WHERE email=? AND platform_admin=1 AND state='active' AND email_verified=1`), string(hash), normalizeEmail(email))
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

// ProvisionUser creates an account whose identity an operator has verified
// outside Dispatch. The first platform administrator must already exist.
func (c *Catalog) ProvisionUser(ctx context.Context, email, name, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return ErrInvalid
	}
	u := User{Email: email, Name: name, State: StateActive, EmailVerified: true}
	if err := prepareUser(&u, c.now()); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	result, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenancy_users(`+userColumns+`) SELECT ?,?,?,?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM tenancy_users WHERE platform_admin=1 AND state='active' AND email_verified=1)`), u.ID, u.Email, u.Name, u.State, 0, 1, string(hash), u.Version, millis(u.CreatedAt))
	if err != nil {
		return conflict(err)
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

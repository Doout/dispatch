package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"time"
)

var ErrAutomationCredential = errors.New("automation identity or credential is inactive")

type AutomationStore interface {
	CreateServiceAccount(context.Context, core.ServiceAccount) error
	GetServiceAccount(context.Context, string) (core.ServiceAccount, error)
	ListServiceAccounts(context.Context) ([]core.ServiceAccount, error)
	UpdateServiceAccount(context.Context, core.ServiceAccount) error
	IssueAutomationCredential(context.Context, core.AutomationCredential, string) error
	RevokeAutomationCredential(context.Context, string, string, time.Time) error
	ListAutomationCredentials(context.Context, string) ([]core.AutomationCredential, error)
	AuthenticateAutomationCredential(context.Context, string, time.Time) (core.ServiceAccount, core.AutomationCredential, error)
	SavePrincipalGrant(context.Context, core.PrincipalGrant) error
	ListPrincipalGrants(context.Context, string, string) ([]core.PrincipalGrant, error)
	DeletePrincipalGrant(context.Context, string, string, string) error
	SaveInfrastructureAssignment(context.Context, core.InfrastructureAssignment) error
	ListInfrastructureAssignments(context.Context, string) ([]core.InfrastructureAssignment, error)
	DeleteInfrastructureAssignment(context.Context, string, string, string) error
}

func (s *SQLStore) CreateServiceAccount(ctx context.Context, a core.ServiceAccount) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO service_accounts(id,name,description,state,created_at,updated_at)VALUES(?,?,?,?,?,?)`), a.ID, a.Name, a.Description, a.State, stamp(a.CreatedAt), stamp(a.UpdatedAt))
	return err
}
func scanServiceAccount(row scanner) (core.ServiceAccount, error) {
	var a core.ServiceAccount
	var c, u string
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.State, &c, &u)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	a.CreatedAt, a.UpdatedAt = parseTime(c), parseTime(u)
	return a, err
}
func (s *SQLStore) GetServiceAccount(ctx context.Context, id string) (core.ServiceAccount, error) {
	return scanServiceAccount(s.db.QueryRowContext(ctx, s.q(`SELECT id,name,description,state,created_at,updated_at FROM service_accounts WHERE id=?`), id))
}
func (s *SQLStore) ListServiceAccounts(ctx context.Context) ([]core.ServiceAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,state,created_at,updated_at FROM service_accounts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.ServiceAccount{}
	for rows.Next() {
		item, err := scanServiceAccount(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (s *SQLStore) UpdateServiceAccount(ctx context.Context, a core.ServiceAccount) error {
	if a.State != "active" && a.State != "disabled" {
		return ErrAutomationCredential
	}
	r, err := s.db.ExecContext(ctx, s.q(`UPDATE service_accounts SET name=?,description=?,state=?,updated_at=? WHERE id=?`), a.Name, a.Description, a.State, stamp(a.UpdatedAt), a.ID)
	return changed(r, err)
}
func (s *SQLStore) IssueAutomationCredential(ctx context.Context, c core.AutomationCredential, retire string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	query := `SELECT state FROM service_accounts WHERE id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	if err = tx.QueryRowContext(ctx, s.q(query), c.AccountID).Scan(&state); err != nil || state != "active" {
		return ErrAutomationCredential
	}
	if !c.ExpiresAt.After(c.CreatedAt) || c.ExpiresAt.After(c.CreatedAt.Add(365*24*time.Hour)) {
		return ErrAutomationCredential
	}
	if retire != "" {
		r, e := tx.ExecContext(ctx, s.q(`UPDATE automation_credentials SET revoked_at=? WHERE id=? AND account_id=? AND revoked_at IS NULL`), stamp(c.CreatedAt), retire, c.AccountID)
		if e != nil {
			return e
		}
		n, e := r.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrAutomationCredential
		}
	}
	_, err = tx.ExecContext(ctx, s.q(`INSERT INTO automation_credentials(id,account_id,name,token_hash,expires_at,created_at)VALUES(?,?,?,?,?,?)`), c.ID, c.AccountID, c.Name, c.TokenHash, stamp(c.ExpiresAt), stamp(c.CreatedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLStore) RevokeAutomationCredential(ctx context.Context, account, id string, now time.Time) error {
	r, err := s.db.ExecContext(ctx, s.q(`UPDATE automation_credentials SET revoked_at=COALESCE(revoked_at,?)WHERE id=? AND account_id=?`), stamp(now), id, account)
	return changed(r, err)
}
func scanAutomationCredential(row scanner) (core.AutomationCredential, error) {
	var c core.AutomationCredential
	var expires, created string
	var revoked, used sql.NullString
	err := row.Scan(&c.ID, &c.AccountID, &c.Name, &c.TokenHash, &expires, &created, &revoked, &used)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	c.ExpiresAt, c.CreatedAt = parseTime(expires), parseTime(created)
	if revoked.Valid {
		v := parseTime(revoked.String)
		c.RevokedAt = &v
	}
	if used.Valid {
		v := parseTime(used.String)
		c.LastUsedAt = &v
	}
	return c, err
}
func (s *SQLStore) ListAutomationCredentials(ctx context.Context, account string) ([]core.AutomationCredential, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id,account_id,name,token_hash,expires_at,created_at,revoked_at,last_used_at FROM automation_credentials WHERE account_id=? ORDER BY created_at`), account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.AutomationCredential{}
	for rows.Next() {
		c, e := scanAutomationCredential(rows)
		if e != nil {
			return nil, e
		}
		c.TokenHash = ""
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s *SQLStore) AuthenticateAutomationCredential(ctx context.Context, hash string, now time.Time) (core.ServiceAccount, core.AutomationCredential, error) {
	var a core.ServiceAccount
	c, err := scanAutomationCredential(s.db.QueryRowContext(ctx, s.q(`SELECT id,account_id,name,token_hash,expires_at,created_at,revoked_at,last_used_at FROM automation_credentials WHERE token_hash=? AND revoked_at IS NULL AND expires_at>? AND EXISTS(SELECT 1 FROM service_accounts WHERE service_accounts.id=automation_credentials.account_id AND state='active')`), hash, stamp(now)))
	if err != nil || !c.ExpiresAt.After(now) {
		return a, c, ErrAutomationCredential
	}
	r, err := s.db.ExecContext(ctx, s.q(`UPDATE automation_credentials SET last_used_at=? WHERE id=? AND revoked_at IS NULL AND expires_at>? AND EXISTS(SELECT 1 FROM service_accounts WHERE service_accounts.id=automation_credentials.account_id AND state='active')`), stamp(now), c.ID, stamp(now))
	if err != nil {
		return a, c, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return a, c, err
	}
	if n != 1 {
		return a, c, ErrAutomationCredential
	}
	a, err = s.GetServiceAccount(ctx, c.AccountID)
	if err != nil || a.State != "active" {
		return a, c, ErrAutomationCredential
	}
	c.TokenHash = ""
	c.LastUsedAt = &now
	return a, c, nil
}
func (s *SQLStore) SavePrincipalGrant(ctx context.Context, g core.PrincipalGrant) error {
	raw, err := json.Marshal(g.Permissions)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO principal_grants(principal_type,principal_id,project_id,permissions,expires_at,updated_at)VALUES(?,?,?,?,?,?)ON CONFLICT(principal_type,principal_id,project_id)DO UPDATE SET permissions=excluded.permissions,expires_at=excluded.expires_at,updated_at=excluded.updated_at`), g.PrincipalType, g.PrincipalID, g.ProjectID, string(raw), nullTime(g.ExpiresAt), stamp(g.UpdatedAt))
	return err
}
func (s *SQLStore) ListPrincipalGrants(ctx context.Context, kind, id string) ([]core.PrincipalGrant, error) {
	query := `SELECT principal_type,principal_id,project_id,permissions,expires_at,updated_at FROM principal_grants`
	args := []any{}
	if kind != "" {
		query += ` WHERE principal_type=? AND principal_id=?`
		args = append(args, kind, id)
	}
	rows, err := s.db.QueryContext(ctx, s.q(query+` ORDER BY project_id,principal_id`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.PrincipalGrant{}
	for rows.Next() {
		var g core.PrincipalGrant
		var raw, updated string
		var expires sql.NullString
		if err = rows.Scan(&g.PrincipalType, &g.PrincipalID, &g.ProjectID, &raw, &expires, &updated); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &g.Permissions); err != nil {
			return nil, err
		}
		g.UpdatedAt = parseTime(updated)
		if expires.Valid {
			v := parseTime(expires.String)
			g.ExpiresAt = &v
		}
		items = append(items, g)
	}
	return items, rows.Err()
}
func (s *SQLStore) DeletePrincipalGrant(ctx context.Context, kind, id, project string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM principal_grants WHERE principal_type=? AND principal_id=? AND project_id=?`), kind, id, project)
	return err
}
func (s *SQLStore) SaveInfrastructureAssignment(ctx context.Context, a core.InfrastructureAssignment) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO infrastructure_assignments(project_id,kind,resource_id,updated_at)VALUES(?,?,?,?)ON CONFLICT(project_id,kind,resource_id)DO UPDATE SET updated_at=excluded.updated_at`), a.ProjectID, a.Kind, a.ResourceID, stamp(a.UpdatedAt))
	return err
}
func (s *SQLStore) ListInfrastructureAssignments(ctx context.Context, project string) ([]core.InfrastructureAssignment, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT project_id,kind,resource_id,updated_at FROM infrastructure_assignments WHERE project_id=? ORDER BY kind,resource_id`), project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.InfrastructureAssignment{}
	for rows.Next() {
		var a core.InfrastructureAssignment
		var updated string
		if err = rows.Scan(&a.ProjectID, &a.Kind, &a.ResourceID, &updated); err != nil {
			return nil, err
		}
		a.UpdatedAt = parseTime(updated)
		items = append(items, a)
	}
	return items, rows.Err()
}
func (s *SQLStore) DeleteInfrastructureAssignment(ctx context.Context, project, kind, id string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM infrastructure_assignments WHERE project_id=? AND kind=? AND resource_id=?`), project, kind, id)
	return err
}

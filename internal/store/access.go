package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) CreateInitialOwner(ctx context.Context, credential AdminCredential, user core.User) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO admin_credentials(id,username,password_hash,created_at) VALUES(1,?,?,?)`), credential.Username, credential.PasswordHash, stamp(credential.CreatedAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO users(id,username,display_name,email,password_hash,system_role,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`),
		user.ID, user.Username, user.DisplayName, user.Email, user.PasswordHash, user.SystemRole, user.State, stamp(user.CreatedAt), stamp(user.UpdatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) CreateUserSession(ctx context.Context, tokenHash, userID string, expiresAt, createdAt time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM admin_sessions WHERE expires_at<=?`), stamp(createdAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO admin_sessions(token_hash,expires_at,created_at,user_id) VALUES(?,?,?,?)`), tokenHash, stamp(expiresAt), stamp(createdAt), userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) SessionUser(ctx context.Context, tokenHash string, now time.Time) (core.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, s.q(`SELECT u.id,u.username,u.display_name,u.email,u.password_hash,u.system_role,u.state,u.created_at,u.updated_at
		FROM admin_sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at>?`), tokenHash, stamp(now)))
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrNotFound
	}
	return user, err
}

func (s *SQLStore) CreateUser(ctx context.Context, user core.User) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO users(id,username,display_name,email,password_hash,system_role,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`),
		user.ID, user.Username, user.DisplayName, user.Email, user.PasswordHash, user.SystemRole, user.State, stamp(user.CreatedAt), stamp(user.UpdatedAt))
	return err
}

func (s *SQLStore) UpdateUser(ctx context.Context, user core.User) error {
	query := `UPDATE users SET username=?,display_name=?,email=?,system_role=?,state=?,updated_at=? WHERE id=?`
	args := []any{user.Username, user.DisplayName, user.Email, user.SystemRole, user.State, stamp(user.UpdatedAt), user.ID}
	if user.PasswordHash != "" {
		query = `UPDATE users SET username=?,display_name=?,email=?,password_hash=?,system_role=?,state=?,updated_at=? WHERE id=?`
		args = []any{user.Username, user.DisplayName, user.Email, user.PasswordHash, user.SystemRole, user.State, stamp(user.UpdatedAt), user.ID}
	}
	return changed(s.db.ExecContext(ctx, s.q(query), args...))
}

func (s *SQLStore) DeleteUser(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM role_assignments WHERE principal_type='user' AND principal_id=?`), id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`DELETE FROM users WHERE id=?`), id)
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) MergeUsers(ctx context.Context, sourceID, targetID string) error {
	if sourceID == targetID {
		return ErrAlreadyExists
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var sourceCount, targetCount int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM users WHERE id=?`), sourceID).Scan(&sourceCount); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM users WHERE id=?`), targetID).Scan(&targetCount); err != nil {
		return err
	}
	if sourceCount == 0 || targetCount == 0 {
		return ErrNotFound
	}

	var localConflicts int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM users source
		JOIN users target ON target.id=?
		WHERE source.id=? AND source.password_hash<>'' AND target.password_hash<>''`), targetID, sourceID).Scan(&localConflicts); err != nil {
		return err
	}
	if localConflicts > 0 {
		return ErrIdentityConflict
	}

	var providerTypeConflicts int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM external_identities source
		JOIN auth_providers source_provider ON source_provider.id=source.provider_id
		JOIN external_identities target ON target.user_id=?
		JOIN auth_providers target_provider ON target_provider.id=target.provider_id
		WHERE source.user_id=? AND source_provider.provider_type=target_provider.provider_type`), targetID, sourceID).Scan(&providerTypeConflicts); err != nil {
		return err
	}
	if providerTypeConflicts > 0 {
		return ErrIdentityConflict
	}

	memberRows, err := tx.QueryContext(ctx, s.q(`SELECT team_id,role,created_at FROM team_members WHERE user_id=?`), sourceID)
	if err != nil {
		return err
	}
	members := []core.TeamMember{}
	for memberRows.Next() {
		var item core.TeamMember
		var created string
		if err := memberRows.Scan(&item.TeamID, &item.Role, &created); err != nil {
			_ = memberRows.Close()
			return err
		}
		item.UserID, item.CreatedAt = targetID, parseTime(created)
		members = append(members, item)
	}
	if err := memberRows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM team_members WHERE user_id=?`), sourceID); err != nil {
		return err
	}
	for _, item := range members {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO team_members(team_id,user_id,role,created_at) VALUES(?,?,?,?) ON CONFLICT(team_id,user_id) DO NOTHING`), item.TeamID, targetID, item.Role, stamp(item.CreatedAt)); err != nil {
			return err
		}
	}

	assignmentRows, err := tx.QueryContext(ctx, s.q(`SELECT id,scope_type,scope_id,role,created_at,updated_at FROM role_assignments WHERE principal_type='user' AND principal_id=?`), sourceID)
	if err != nil {
		return err
	}
	assignments := []core.RoleAssignment{}
	for assignmentRows.Next() {
		var item core.RoleAssignment
		var created, updated string
		if err := assignmentRows.Scan(&item.ID, &item.ScopeType, &item.ScopeID, &item.Role, &created, &updated); err != nil {
			_ = assignmentRows.Close()
			return err
		}
		item.PrincipalType, item.PrincipalID = core.PrincipalUser, targetID
		item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
		assignments = append(assignments, item)
	}
	if err := assignmentRows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM role_assignments WHERE principal_type='user' AND principal_id=?`), sourceID); err != nil {
		return err
	}
	for _, item := range assignments {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO role_assignments(id,principal_type,principal_id,scope_type,scope_id,role,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(principal_type,principal_id,scope_type,scope_id) DO NOTHING`), item.ID, item.PrincipalType, item.PrincipalID, item.ScopeType, item.ScopeID, item.Role, stamp(item.CreatedAt), stamp(item.UpdatedAt)); err != nil {
			return err
		}
	}

	if _, err := tx.ExecContext(ctx, s.q(`UPDATE external_identities SET user_id=? WHERE user_id=?`), targetID, sourceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM admin_sessions WHERE user_id=?`), sourceID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`DELETE FROM users WHERE id=?`), sourceID)
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) GetUser(ctx context.Context, id string) (core.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, s.q(`SELECT id,username,display_name,email,password_hash,system_role,state,created_at,updated_at FROM users WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrNotFound
	}
	return user, err
}

func (s *SQLStore) GetUserByUsername(ctx context.Context, username string) (core.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, s.q(`SELECT id,username,display_name,email,password_hash,system_role,state,created_at,updated_at FROM users WHERE username=?`), username))
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrNotFound
	}
	return user, err
}

func (s *SQLStore) GetUserByEmail(ctx context.Context, email string) (core.User, error) {
	user, err := scanUser(s.db.QueryRowContext(ctx, s.q(`SELECT id,username,display_name,email,password_hash,system_role,state,created_at,updated_at FROM users WHERE LOWER(email)=LOWER(?)`), email))
	if errors.Is(err, sql.ErrNoRows) {
		return user, ErrNotFound
	}
	return user, err
}

func (s *SQLStore) ListUsers(ctx context.Context) ([]core.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,username,display_name,email,password_hash,system_role,state,created_at,updated_at FROM users ORDER BY display_name,username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.User{}
	for rows.Next() {
		item, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanUser(row scanner) (core.User, error) {
	var user core.User
	var created, updated string
	err := row.Scan(&user.ID, &user.Username, &user.DisplayName, &user.Email, &user.PasswordHash, &user.SystemRole, &user.State, &created, &updated)
	user.PasswordConfigured = user.PasswordHash != ""
	user.CreatedAt, user.UpdatedAt = parseTime(created), parseTime(updated)
	return user, err
}

func (s *SQLStore) CountOwners(ctx context.Context) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE system_role='owner' AND state='active'`).Scan(&count)
	return count, err
}

func (s *SQLStore) CreateTeam(ctx context.Context, team core.Team) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO teams(id,name,description,created_at,updated_at) VALUES(?,?,?,?,?)`), team.ID, team.Name, team.Description, stamp(team.CreatedAt), stamp(team.UpdatedAt))
	return err
}

func (s *SQLStore) UpdateTeam(ctx context.Context, team core.Team) error {
	return changed(s.db.ExecContext(ctx, s.q(`UPDATE teams SET name=?,description=?,updated_at=? WHERE id=?`), team.Name, team.Description, stamp(team.UpdatedAt), team.ID))
}

func (s *SQLStore) DeleteTeam(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM role_assignments WHERE principal_type='team' AND principal_id=?`), id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, s.q(`DELETE FROM teams WHERE id=?`), id)
	if err := changed(result, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLStore) GetTeam(ctx context.Context, id string) (core.Team, error) {
	team, err := scanTeam(s.db.QueryRowContext(ctx, s.q(`SELECT id,name,description,created_at,updated_at FROM teams WHERE id=?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return team, ErrNotFound
	}
	return team, err
}

func (s *SQLStore) ListTeams(ctx context.Context) ([]core.Team, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,description,created_at,updated_at FROM teams ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.Team{}
	for rows.Next() {
		item, err := scanTeam(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanTeam(row scanner) (core.Team, error) {
	var team core.Team
	var created, updated string
	err := row.Scan(&team.ID, &team.Name, &team.Description, &created, &updated)
	team.CreatedAt, team.UpdatedAt = parseTime(created), parseTime(updated)
	return team, err
}

func (s *SQLStore) ReplaceTeamMembers(ctx context.Context, teamID string, members []core.TeamMember) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM team_members WHERE team_id=?`), teamID); err != nil {
		return err
	}
	for _, member := range members {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO team_members(team_id,user_id,role,created_at) VALUES(?,?,?,?)`), teamID, member.UserID, member.Role, stamp(member.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLStore) ListTeamMembers(ctx context.Context) ([]core.TeamMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT team_id,user_id,role,created_at FROM team_members ORDER BY team_id,created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.TeamMember{}
	for rows.Next() {
		var item core.TeamMember
		var created string
		if err := rows.Scan(&item.TeamID, &item.UserID, &item.Role, &created); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(created)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) UpsertRoleAssignment(ctx context.Context, item core.RoleAssignment) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO role_assignments(id,principal_type,principal_id,scope_type,scope_id,role,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(principal_type,principal_id,scope_type,scope_id)
		DO UPDATE SET role=excluded.role,updated_at=excluded.updated_at`), item.ID, item.PrincipalType, item.PrincipalID, item.ScopeType, item.ScopeID, item.Role, stamp(item.CreatedAt), stamp(item.UpdatedAt))
	return err
}

func (s *SQLStore) DeleteRoleAssignment(ctx context.Context, id string) error {
	return changed(s.db.ExecContext(ctx, s.q(`DELETE FROM role_assignments WHERE id=?`), id))
}

func (s *SQLStore) ListRoleAssignments(ctx context.Context) ([]core.RoleAssignment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,principal_type,principal_id,scope_type,scope_id,role,created_at,updated_at FROM role_assignments ORDER BY scope_id,principal_type,principal_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []core.RoleAssignment{}
	for rows.Next() {
		var item core.RoleAssignment
		var created, updated string
		if err := rows.Scan(&item.ID, &item.PrincipalType, &item.PrincipalID, &item.ScopeType, &item.ScopeID, &item.Role, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt, item.UpdatedAt = parseTime(created), parseTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func (s *SQLStore) AppendAuditEvent(ctx context.Context, e core.AuditEvent) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO audit_events(id,actor_id,actor_name,impersonator_id,project_id,app_id,action,resource_id,outcome,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`), e.ID, e.ActorID, e.ActorName, e.ImpersonatorID, e.ProjectID, e.AppID, e.Action, e.ResourceID, e.Outcome, stamp(e.CreatedAt))
	return err
}
func (s *SQLStore) ListAuditEvents(ctx context.Context, f core.AuditFilter) ([]core.AuditEvent, error) {
	q := `SELECT id,actor_id,actor_name,impersonator_id,project_id,app_id,action,resource_id,outcome,created_at FROM audit_events WHERE 1=1`
	args := []any{}
	if f.ProjectIDs != nil {
		q += ` AND project_id IN (`
		for i, id := range f.ProjectIDs {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, id)
		}
		if len(f.ProjectIDs) == 0 {
			q += "NULL"
		}
		q += ")"
	}
	for _, v := range []struct{ k, v string }{{"app_id", f.AppID}, {"actor_id", f.ActorID}, {"action", f.Action}} {
		if v.v != "" {
			q += " AND " + v.k + "=?"
			args = append(args, v.v)
		}
	}
	if f.Before != "" {
		q += " AND id<?"
		args = append(args, f.Before)
	}
	if f.Limit < 1 || f.Limit > 200 {
		f.Limit = 100
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, s.q(q), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.AuditEvent{}
	for rows.Next() {
		var e core.AuditEvent
		var at string
		if err = rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.ImpersonatorID, &e.ProjectID, &e.AppID, &e.Action, &e.ResourceID, &e.Outcome, &at); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTime(at)
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *SQLStore) GetApplicationOwner(ctx context.Context, id string) (core.ApplicationOwner, error) {
	var o core.ApplicationOwner
	var at string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT app_id,principal_type,principal_id,updated_at FROM application_owners WHERE app_id=?`), id).Scan(&o.AppID, &o.PrincipalType, &o.PrincipalID, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return core.ApplicationOwner{AppID: id}, nil
	}
	o.UpdatedAt = parseTime(at)
	return o, err
}
func (s *SQLStore) SaveApplicationOwner(ctx context.Context, o core.ApplicationOwner) error {
	if o.PrincipalID == "" {
		_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM application_owners WHERE app_id=?`), o.AppID)
		return err
	}
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO application_owners(app_id,principal_type,principal_id,updated_at) VALUES(?,?,?,?) ON CONFLICT(app_id) DO UPDATE SET principal_type=excluded.principal_type,principal_id=excluded.principal_id,updated_at=excluded.updated_at`), o.AppID, o.PrincipalType, o.PrincipalID, stamp(o.UpdatedAt))
	return err
}
func (s *SQLStore) ListIdentityTeamMappings(ctx context.Context) ([]core.IdentityTeamMapping, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,provider_id,external_group,team_id FROM identity_team_mappings ORDER BY provider_id,external_group`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.IdentityTeamMapping{}
	for rows.Next() {
		var m core.IdentityTeamMapping
		if err = rows.Scan(&m.ID, &m.ProviderID, &m.ExternalGroup, &m.TeamID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *SQLStore) SaveIdentityTeamMapping(ctx context.Context, m core.IdentityTeamMapping) error {
	_, err := s.db.ExecContext(ctx, s.q(`INSERT INTO identity_team_mappings(id,provider_id,external_group,team_id) VALUES(?,?,?,?) ON CONFLICT(provider_id,external_group,team_id) DO NOTHING`), m.ID, m.ProviderID, m.ExternalGroup, m.TeamID)
	return err
}
func (s *SQLStore) DeleteIdentityTeamMapping(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM identity_team_mappings WHERE id=?`), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM identity_team_members WHERE NOT EXISTS (SELECT 1 FROM identity_team_mappings m WHERE m.provider_id=identity_team_members.provider_id AND m.team_id=identity_team_members.team_id)`); err != nil {
		return err
	}
	return tx.Commit()
}

// Provider-managed memberships stay separate from manually granted team membership.
func (s *SQLStore) SyncIdentityTeamMemberships(ctx context.Context, provider, user string, groups []string) error {
	mappings, err := s.ListIdentityTeamMappings(ctx)
	if err != nil {
		return err
	}
	matched := map[string]bool{}
	for _, g := range groups {
		matched[g] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM identity_team_members WHERE provider_id=? AND user_id=?`), provider, user); err != nil {
		return err
	}
	for _, m := range mappings {
		if m.ProviderID == provider && matched[m.ExternalGroup] {
			if _, err = tx.ExecContext(ctx, s.q(`INSERT INTO identity_team_members(mapping_id,provider_id,team_id,user_id) VALUES(?,?,?,?) ON CONFLICT DO NOTHING`), m.ID, provider, m.TeamID, user); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
func (s *SQLStore) ListIdentityTeamMembers(ctx context.Context) ([]core.TeamMember, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT m.team_id,m.user_id FROM identity_team_members m JOIN auth_providers p ON p.id=m.provider_id WHERE p.state='ready' AND EXISTS (SELECT 1 FROM external_identities e WHERE e.provider_id=m.provider_id AND e.user_id=m.user_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.TeamMember{}
	for rows.Next() {
		var m core.TeamMember
		if err = rows.Scan(&m.TeamID, &m.UserID); err != nil {
			return nil, err
		}
		m.Role = core.TeamMemberRoleMember
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *SQLStore) SaveRetentionPolicy(ctx context.Context, p core.RetentionPolicy) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO retention_policies(project_id,payload) VALUES(?,?) ON CONFLICT(project_id) DO UPDATE SET payload=excluded.payload`), p.ProjectID, string(b))
	return err
}
func (s *SQLStore) GetRetentionPolicy(ctx context.Context, id string) (core.RetentionPolicy, error) {
	var b string
	p := core.RetentionPolicy{ProjectID: id, LogDays: 30, RunDays: 90, KeepRuns: 20}
	err := s.db.QueryRowContext(ctx, s.q(`SELECT payload FROM retention_policies WHERE project_id=?`), id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal([]byte(b), &p)
}
func (s *SQLStore) SaveBackupRecord(ctx context.Context, b core.BackupRecord) error {
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, s.q(`INSERT INTO controller_backups(id,payload) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`), b.ID, string(raw))
	return err
}
func (s *SQLStore) ListBackupRecords(ctx context.Context) ([]core.BackupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM controller_backups ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.BackupRecord{}
	for rows.Next() {
		var b string
		var v core.BackupRecord
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(b), &v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *SQLStore) DatabaseEngine() string {
	if s.postgres {
		return "postgresql"
	}
	return "sqlite"
}
func (s *SQLStore) BackupSQLite(ctx context.Context, path string) error {
	if s.postgres {
		return errors.New("use pg_dump for PostgreSQL backups")
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
func (s *SQLStore) CheckSQLite(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errors.New("restored database integrity check failed")
	}
	var count int
	return s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count)
}

// Retention never removes a successful release, a baseline, an active run, or a run linked to a workflow/preview.
// Retained runtime release history can still refer to that run's binding credentials.
func (s *SQLStore) ApplyRetention(ctx context.Context, p core.RetentionPolicy, apply bool, now time.Time) (core.RetentionResult, error) {
	out := core.RetentionResult{Applied: apply}
	if p.LogDays < 1 || p.RunDays < 1 || p.KeepRuns < 1 {
		return out, errors.New("retention limits must be positive")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.q(`SELECT d.id,d.app_id,d.state,d.created_at,
 CASE WHEN EXISTS(SELECT 1 FROM workflow_stage_runs w WHERE w.deployment_ids LIKE '%' || d.id || '%') OR EXISTS(SELECT 1 FROM preview_group_run_components p WHERE p.deployment_id=d.id) OR EXISTS(SELECT 1 FROM deployment_drift_baselines b WHERE b.deployment_id=d.id) THEN 1 ELSE 0 END
 FROM deployments d JOIN apps a ON a.id=d.app_id WHERE a.project_id=? ORDER BY d.created_at DESC,d.id DESC`), p.ProjectID)
	if err != nil {
		return out, err
	}
	type run struct {
		id, app, state string
		at             time.Time
		linked         int
	}
	runs := []run{}
	for rows.Next() {
		var r run
		var at string
		if err = rows.Scan(&r.id, &r.app, &r.state, &at, &r.linked); err != nil {
			rows.Close()
			return out, err
		}
		r.at = parseTime(at)
		runs = append(runs, r)
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	counts := map[string]int{}
	live := map[string]bool{}
	for _, r := range runs {
		counts[r.app]++
		terminal := core.DeploymentState(r.state).Terminal()
		keep := !terminal || counts[r.app] <= p.KeepRuns || r.state == string(core.DeploymentSucceeded) || r.linked == 1
		latestSuccess := r.state == string(core.DeploymentSucceeded) && !live[r.app]
		if r.state == string(core.DeploymentSucceeded) {
			live[r.app] = true
		}
		if keep {
			out.ProtectedRuns++
		}
		if terminal && !latestSuccess {
			var n int64
			if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM deployment_logs WHERE deployment_id=? AND created_at<?`), r.id, stamp(now.AddDate(0, 0, -p.LogDays))).Scan(&n); err != nil {
				return out, err
			}
			out.Logs += n
			if apply {
				if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployment_logs WHERE deployment_id=? AND created_at<?`), r.id, stamp(now.AddDate(0, 0, -p.LogDays))); err != nil {
					return out, err
				}
			}
		}
		if !keep && r.at.Before(now.AddDate(0, 0, -p.RunDays)) {
			out.Runs++
			if apply {
				if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployment_logs WHERE deployment_id=?`), r.id); err != nil {
					return out, err
				}
				if _, err = tx.ExecContext(ctx, s.q(`DELETE FROM deployments WHERE id=? AND state IN ('failed','cancelled')`), r.id); err != nil {
					return out, err
				}
			}
		}
	}
	if apply {
		err = tx.Commit()
	}
	return out, err
}

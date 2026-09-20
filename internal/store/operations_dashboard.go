package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// UTC timestamps are stored as RFC3339Nano text with optional fractional digits.
func operationsTimeOrder(expression string) string {
	return `CASE WHEN length(` + expression + `)=20 THEN substr(` + expression + `,1,19)||'.000000000Z' ELSE substr(replace(` + expression + `,'Z','')||'000000000',1,29)||'Z' END`
}
func operationsStamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func scopedOperationsWhere(column string, projects []string) (string, []any) {
	if projects == nil {
		return "1=1", nil
	}
	if len(projects) == 0 {
		return "1=0", nil
	}
	values := make([]string, len(projects))
	args := make([]any, len(projects))
	for i, id := range projects {
		values[i] = "?"
		args[i] = id
	}
	return column + " IN (" + strings.Join(values, ",") + ")", args
}
func operationsSearch(value string) string {
	return "%" + strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(value)) + "%"
}
func auditWhere(f core.AuditFilter) (string, []any) {
	where, args := scopedOperationsWhere("audit_events.project_id", f.ProjectIDs)
	for _, v := range []struct{ column, value string }{{"app_id", f.AppID}, {"actor_id", f.ActorID}, {"action", f.Action}, {"outcome", f.Outcome}} {
		if v.value != "" {
			where += " AND " + v.column + "=?"
			args = append(args, v.value)
		}
	}
	if f.Before != "" {
		where += " AND id<?"
		args = append(args, f.Before)
	}
	if f.Since != nil {
		where += " AND (" + operationsTimeOrder("created_at") + ")>=?"
		args = append(args, operationsStamp(*f.Since))
	}
	if f.Until != nil {
		where += " AND (" + operationsTimeOrder("created_at") + ")<?"
		args = append(args, operationsStamp(*f.Until))
	}
	if f.Query != "" {
		term := operationsSearch(f.Query)
		columns := []string{"actor_id", "actor_name", "impersonator_id", "action", "resource_id", "app_id", "project_id"}
		matches := []string{}
		for _, c := range columns {
			matches = append(matches, "LOWER("+c+") LIKE ? ESCAPE '!'")
			args = append(args, term)
		}
		// An application can move projects. Never use its new private name as a
		// search oracle for an event scoped to its old project.
		matches = append(matches, `EXISTS (SELECT 1 FROM apps a WHERE a.id=audit_events.app_id AND a.project_id=audit_events.project_id AND LOWER(a.name) LIKE ? ESCAPE '!')`, `EXISTS (SELECT 1 FROM projects p WHERE p.id=audit_events.project_id AND LOWER(p.name) LIKE ? ESCAPE '!')`)
		args = append(args, term, term)
		where += " AND (" + strings.Join(matches, " OR ") + ")"
	}
	return where, args
}

func (s *SQLStore) GetOperationsSummary(ctx context.Context, projects []string, backups bool, now time.Time) (core.OperationsSummary, error) {
	now = now.UTC()
	since := now.Add(-24 * time.Hour)
	out := core.OperationsSummary{ObservedAt: now, Audit: core.OperationsAuditSummary{Since: since, Recent: []core.AuditEvent{}}}
	options := &sql.TxOptions{ReadOnly: true}
	if s.postgres {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.BeginTx(ctx, options)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	where, args := auditWhere(core.AuditFilter{ProjectIDs: projects, Since: &since, Until: &now})
	if err = tx.QueryRowContext(ctx, s.q(`SELECT count(*),coalesce(sum(CASE WHEN outcome='rejected' THEN 1 ELSE 0 END),0) FROM audit_events WHERE `+where), args...).Scan(&out.Audit.Total, &out.Audit.Rejected); err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, s.q(`SELECT id,actor_id,actor_name,impersonator_id,project_id,app_id,action,resource_id,outcome,created_at FROM audit_events WHERE `+where+` ORDER BY `+operationsTimeOrder("created_at")+` DESC,id DESC LIMIT 5`), args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e core.AuditEvent
		var at string
		if err = rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.ImpersonatorID, &e.ProjectID, &e.AppID, &e.Action, &e.ResourceID, &e.Outcome, &at); err != nil {
			rows.Close()
			return out, err
		}
		e.CreatedAt = parseTime(at)
		out.Audit.Recent = append(out.Audit.Recent, e)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	if err = rows.Close(); err != nil {
		return out, err
	}
	scope, args := scopedOperationsWhere("a.project_id", projects)
	if err = tx.QueryRowContext(ctx, s.q(`SELECT count(*),coalesce(sum(CASE WHEN o.principal_id IS NULL OR o.principal_id='' THEN 1 ELSE 0 END),0) FROM apps a LEFT JOIN application_owners o ON o.app_id=a.id WHERE a.state<>'closed' AND NOT a.template AND `+scope), args...).Scan(&out.Ownership.Total, &out.Ownership.Unassigned); err != nil {
		return out, err
	}
	if backups {
		out.Backups = &core.BackupSummary{}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM controller_backups`).Scan(&out.Backups.Recorded); err != nil {
			return out, err
		}
		field := func(name string) string {
			if s.postgres {
				return "payload::jsonb->>'" + name + "'"
			}
			return "json_extract(payload,'$." + name + "')"
		}
		for _, spec := range []struct {
			target           **core.BackupRecord
			condition, order string
		}{{&out.Backups.Latest, "1=1", field("createdAt")}, {&out.Backups.LatestVerified, field("state") + "='verified' AND " + field("verifiedAt") + " IS NOT NULL", field("verifiedAt")}} {
			var payload string
			err = tx.QueryRowContext(ctx, `SELECT payload FROM controller_backups WHERE `+spec.condition+` ORDER BY `+operationsTimeOrder(spec.order)+` DESC,id DESC LIMIT 1`).Scan(&payload)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return out, err
			}
			var record core.BackupRecord
			if err = json.Unmarshal([]byte(payload), &record); err != nil {
				return out, err
			}
			*spec.target = &record
		}
	}
	// This transaction only reads; do not broadcast a committed-write event.
	return out, tx.Tx.Commit()
}

const ownershipDisplay = `CASE WHEN o.principal_type='user' THEN coalesce(nullif(u.display_name,''),u.username,'') WHEN o.principal_type='team' THEN coalesce(t.name,'') ELSE '' END`

func (s *SQLStore) ListOperationsOwnership(ctx context.Context, f core.OwnershipFilter) ([]core.OwnershipItem, error) {
	scope, args := scopedOperationsWhere("a.project_id", f.ProjectIDs)
	query := `SELECT a.id,a.name,a.project_id,coalesce(o.principal_type,''),coalesce(o.principal_id,''),` + ownershipDisplay + `,coalesce(o.updated_at,'') FROM apps a LEFT JOIN application_owners o ON o.app_id=a.id LEFT JOIN users u ON o.principal_type='user' AND u.id=o.principal_id LEFT JOIN teams t ON o.principal_type='team' AND t.id=o.principal_id WHERE a.state<>'closed' AND NOT a.template AND ` + scope
	if f.Unassigned {
		query += ` AND (o.principal_id IS NULL OR o.principal_id='')`
	} else if f.Assigned {
		query += ` AND o.principal_id IS NOT NULL AND o.principal_id<>''`
	}
	if f.Query != "" {
		term := operationsSearch(f.Query)
		query += ` AND (LOWER(a.name) LIKE ? ESCAPE '!' OR LOWER(a.id) LIKE ? ESCAPE '!' OR LOWER(coalesce(o.principal_id,'')) LIKE ? ESCAPE '!' OR LOWER(` + ownershipDisplay + `) LIKE ? ESCAPE '!')`
		args = append(args, term, term, term, term)
	}
	if f.BeforeID != "" {
		query += ` AND (LOWER(a.name)>LOWER(?) OR (LOWER(a.name)=LOWER(?) AND a.id>?))`
		args = append(args, f.BeforeName, f.BeforeName, f.BeforeID)
	}
	if f.Limit < 1 || f.Limit > 101 {
		f.Limit = 101
	}
	query += ` ORDER BY LOWER(a.name),a.id LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.OwnershipItem{}
	for rows.Next() {
		var item core.OwnershipItem
		var owner core.OwnershipPrincipal
		var at string
		if err = rows.Scan(&item.AppID, &item.AppName, &item.ProjectID, &owner.PrincipalType, &owner.PrincipalID, &owner.DisplayName, &at); err != nil {
			return nil, err
		}
		if owner.PrincipalID != "" {
			owner.UpdatedAt = parseTime(at)
			item.Owner = &owner
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

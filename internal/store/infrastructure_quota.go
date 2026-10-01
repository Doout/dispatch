package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrQuotaPolicyChanged = errors.New("project infrastructure policy changed")
var ErrQuotaReservationChanged = errors.New("infrastructure reservation ownership changed")

type InfrastructureQuotaStore interface {
	GetInfrastructureQuotaPolicy(context.Context, string) (core.InfrastructureQuotaPolicy, error)
	SaveInfrastructureQuotaPolicy(context.Context, core.InfrastructureQuotaPolicy, int64) error
	ListInfrastructureQuotaReservations(context.Context, string) ([]core.InfrastructureQuotaReservation, error)
	ApplyInfrastructureQuota(context.Context, *sql.Tx, core.InfrastructureQuotaChange, time.Time) error
}

func scanQuotaPolicy(row scanner) (core.InfrastructureQuotaPolicy, error) {
	var p core.InfrastructureQuotaPolicy
	var raw, updated string
	err := row.Scan(&p.ProjectID, &p.Revision, &p.MaxServers, &p.MaxTemporaryEnvironments, &p.MaxSnapshots, &p.MaxTemporaryLifetimeSeconds, &raw, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p.Providers); err != nil {
		return p, err
	}
	p.Configured = true
	p.UpdatedAt = parseTime(updated)
	return p, nil
}

const quotaPolicyColumns = `project_id,revision,max_servers,max_temporary_environments,max_snapshots,max_temporary_lifetime_seconds,providers,updated_at`

func (s *SQLStore) GetInfrastructureQuotaPolicy(ctx context.Context, project string) (core.InfrastructureQuotaPolicy, error) {
	p, err := scanQuotaPolicy(s.db.QueryRowContext(ctx, s.q(`SELECT `+quotaPolicyColumns+` FROM project_infrastructure_policies WHERE project_id=?`), project))
	if errors.Is(err, ErrNotFound) {
		return core.InfrastructureQuotaPolicy{ProjectID: project, Providers: []core.InfrastructureProviderRule{}}, nil
	}
	return p, err
}
func (s *SQLStore) SaveInfrastructureQuotaPolicy(ctx context.Context, p core.InfrastructureQuotaPolicy, expected int64) error {
	if expected < 0 || p.Revision != expected+1 || p.MaxServers < -1 || p.MaxTemporaryEnvironments < -1 || p.MaxSnapshots < -1 || p.MaxTemporaryLifetimeSeconds < 0 {
		return ErrQuotaPolicyChanged
	}
	raw, err := json.Marshal(p.Providers)
	if err != nil {
		return err
	}
	var result sql.Result
	if expected == 0 {
		result, err = s.db.ExecContext(ctx, s.q(`INSERT INTO project_infrastructure_policies(`+quotaPolicyColumns+`)VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(project_id) DO NOTHING`), p.ProjectID, p.Revision, p.MaxServers, p.MaxTemporaryEnvironments, p.MaxSnapshots, p.MaxTemporaryLifetimeSeconds, string(raw), stamp(p.UpdatedAt))
	} else {
		result, err = s.db.ExecContext(ctx, s.q(`UPDATE project_infrastructure_policies SET revision=?,max_servers=?,max_temporary_environments=?,max_snapshots=?,max_temporary_lifetime_seconds=?,providers=?,updated_at=? WHERE project_id=? AND revision=?`), p.Revision, p.MaxServers, p.MaxTemporaryEnvironments, p.MaxSnapshots, p.MaxTemporaryLifetimeSeconds, string(raw), stamp(p.UpdatedAt), p.ProjectID, expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n != 1 {
		return ErrQuotaPolicyChanged
	}
	return err
}

const quotaReservationColumns = `server_id,operation_id,project_id,provider_id,region,size,state,resource_id,created_at,updated_at`

func scanQuotaReservation(row scanner) (core.InfrastructureQuotaReservation, error) {
	var r core.InfrastructureQuotaReservation
	var created, updated string
	err := row.Scan(&r.ServerID, &r.OperationID, &r.ProjectID, &r.ProviderID, &r.Region, &r.Size, &r.State, &r.ResourceID, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	r.CreatedAt, r.UpdatedAt = parseTime(created), parseTime(updated)
	return r, err
}
func (s *SQLStore) ListInfrastructureQuotaReservations(ctx context.Context, project string) ([]core.InfrastructureQuotaReservation, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT `+quotaReservationColumns+` FROM infrastructure_quota_reservations WHERE project_id=? ORDER BY created_at,server_id`), project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.InfrastructureQuotaReservation{}
	for rows.Next() {
		r, e := scanQuotaReservation(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApplyInfrastructureQuota joins the caller's acceptance transaction. A server
// stays counted across provider uncertainty or failed enrollment. Only an
// authoritative terminal event can release its reservation.
func (s *SQLStore) ApplyInfrastructureQuota(ctx context.Context, tx *sql.Tx, c core.InfrastructureQuotaChange, now time.Time) error {
	if tx == nil || c.ProjectID == "" || c.ProviderID == "" || c.ServerID == "" || c.OperationID == "" {
		return ErrQuotaReservationChanged
	}
	switch c.Action {
	case "server.create":
		// Serialize project admissions before counting, including across controllers.
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE project_infrastructure_policies SET revision=revision WHERE project_id=?`), c.ProjectID); err != nil {
			return err
		}
		existing, err := scanQuotaReservation(tx.QueryRowContext(ctx, s.q(`SELECT `+quotaReservationColumns+` FROM infrastructure_quota_reservations WHERE server_id=?`), c.ServerID))
		if err == nil {
			if existing.ProjectID != c.ProjectID || existing.ProviderID != c.ProviderID || existing.OperationID != c.OperationID || existing.Region != c.Region || existing.Size != c.Size || existing.State == "released" {
				return ErrQuotaReservationChanged
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		p, err := scanQuotaPolicy(tx.QueryRowContext(ctx, s.q(`SELECT `+quotaPolicyColumns+` FROM project_infrastructure_policies WHERE project_id=?`), c.ProjectID))
		if errors.Is(err, ErrNotFound) {
			return &core.InfrastructureQuotaViolation{Code: "policy_required", Limit: "maxServers", Maximum: 0, Requested: 1}
		}
		if err != nil {
			return err
		}
		violation := &core.InfrastructureQuotaViolation{Code: "allocation_disallowed", Requested: 1, ProviderID: c.ProviderID, Region: c.Region, Size: c.Size}
		var rule *core.InfrastructureProviderRule
		for i := range p.Providers {
			if p.Providers[i].ProviderID == c.ProviderID {
				rule = &p.Providers[i]
				break
			}
		}
		if rule == nil {
			violation.Limit = "provider"
			return violation
		}
		if !rule.AnyRegion && !slices.Contains(rule.Regions, c.Region) {
			violation.Limit = "region"
			return violation
		}
		if !rule.AnySize && !slices.Contains(rule.Sizes, c.Size) {
			violation.Limit = "size"
			return violation
		}
		var used int64
		if err = tx.QueryRowContext(ctx, s.q(`SELECT COUNT(*) FROM infrastructure_quota_reservations WHERE project_id=? AND state<>'released'`), c.ProjectID).Scan(&used); err != nil {
			return err
		}
		if p.MaxServers >= 0 && used+1 > p.MaxServers {
			return &core.InfrastructureQuotaViolation{Code: "quota_exceeded", Limit: "maxServers", Maximum: p.MaxServers, Usage: used, Requested: 1}
		}
		_, err = tx.ExecContext(ctx, s.q(`INSERT INTO infrastructure_quota_reservations(`+quotaReservationColumns+`)VALUES(?,?,?,?,?,?,'reserved','',?,?)`), c.ServerID, c.OperationID, c.ProjectID, c.ProviderID, c.Region, c.Size, stamp(now), stamp(now))
		return err
	case "server.delete":
		return nil // A request to delete is not proof of deletion.
	case "server.created", "server.deleted", "server.cancelled", "server.unresolved":
		existing, err := scanQuotaReservation(tx.QueryRowContext(ctx, s.q(`SELECT `+quotaReservationColumns+` FROM infrastructure_quota_reservations WHERE server_id=?`), c.ServerID))
		if errors.Is(err, ErrNotFound) {
			return nil
		} // Resources accepted before quotas keep their original ownership record.
		if err != nil {
			return err
		}
		if existing.ProjectID != c.ProjectID || existing.ProviderID != c.ProviderID {
			return ErrQuotaReservationChanged
		}
		state := existing.State
		switch c.Action {
		case "server.created":
			if state == "released" {
				return ErrQuotaReservationChanged
			}
			state = "allocated"
		case "server.deleted":
			state = "released"
		case "server.cancelled":
			if state != "reserved" || existing.ResourceID != "" || existing.OperationID != c.OperationID {
				return ErrQuotaReservationChanged
			}
			state = "released"
		case "server.unresolved":
			if state != "released" {
				state = "unknown"
			}
		}
		resource := existing.ResourceID
		if c.ResourceID != "" {
			if resource != "" && resource != c.ResourceID {
				return ErrQuotaReservationChanged
			}
			resource = c.ResourceID
		}
		_, err = tx.ExecContext(ctx, s.q(`UPDATE infrastructure_quota_reservations SET state=?,resource_id=?,updated_at=? WHERE server_id=? AND project_id=? AND provider_id=?`), state, resource, stamp(now), c.ServerID, c.ProjectID, c.ProviderID)
		return err
	default:
		return ErrQuotaReservationChanged
	}
}

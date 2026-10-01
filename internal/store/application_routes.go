package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/core"
)

var ErrRouteConflict = errors.New("hostname is already reserved or route ownership changed")

type ApplicationRouteStore interface {
	ReserveApplicationRoute(context.Context, core.ApplicationRoute) (core.ApplicationRoute, error)
	SaveApplicationRoute(context.Context, core.ApplicationRoute) error
	SaveApplicationRouteObservation(context.Context, core.ApplicationRoute, core.ApplicationRoute) error
	GetApplicationRoute(context.Context, string) (core.ApplicationRoute, error)
	ListApplicationRoutes(context.Context) ([]core.ApplicationRoute, error)
	DeleteApplicationRoute(context.Context, string, string) error
}

func (s *SQLStore) ReserveApplicationRoute(ctx context.Context, plan core.ApplicationRoute) (core.ApplicationRoute, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return plan, err
	}
	defer tx.Rollback()
	var current core.ApplicationRoute
	var raw string
	err = tx.QueryRowContext(ctx, s.q(`SELECT record FROM application_routes WHERE app_id=?`), plan.AppID).Scan(&raw)
	if err == nil {
		if json.Unmarshal([]byte(raw), &current) != nil {
			return plan, ErrRouteConflict
		}
		if current.Hostname != plan.Hostname || current.ProjectID != plan.ProjectID || current.ServerID != plan.ServerID {
			return plan, ErrRouteConflict
		}
		if current.DeploymentID != "" && (current.EntryPoint != plan.EntryPoint || current.RequireTLS != plan.RequireTLS || current.TLSResolver != plan.TLSResolver) {
			return plan, ErrRouteConflict
		}
		if current.RequestedDeploymentID == plan.RequestedDeploymentID {
			return current, tx.Commit()
		}
		plan.DeploymentID, plan.Destination = current.DeploymentID, current.Destination
		plan.PreviousDeploymentID, plan.PreviousDestination = current.PreviousDeploymentID, current.PreviousDestination
		plan.PublishedAt = current.PublishedAt
		if current.Destination != "" {
			plan.State = "preparing"
			plan.Message = "Previous healthy destination remains active while the candidate is checked."
		}
		_, err = tx.ExecContext(ctx, s.q(`UPDATE application_routes SET requested_deployment_id=?,record=? WHERE app_id=? AND project_id=? AND server_id=? AND hostname=?`), plan.RequestedDeploymentID, jsonText(plan), plan.AppID, plan.ProjectID, plan.ServerID, plan.Hostname)
	} else if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, s.q(`INSERT INTO application_routes(app_id,project_id,server_id,hostname,requested_deployment_id,record) VALUES(?,?,?,?,?,?) ON CONFLICT DO NOTHING`), plan.AppID, plan.ProjectID, plan.ServerID, plan.Hostname, plan.RequestedDeploymentID, jsonText(plan))
		if err != nil {
			return plan, err
		}
		var owner string
		if err = tx.QueryRowContext(ctx, s.q(`SELECT app_id FROM application_routes WHERE hostname=?`), plan.Hostname).Scan(&owner); err != nil || owner != plan.AppID {
			return plan, ErrRouteConflict
		}
	} else {
		return plan, err
	}
	if err != nil {
		return plan, err
	}
	return plan, tx.Commit()
}

func (s *SQLStore) SaveApplicationRoute(ctx context.Context, route core.ApplicationRoute) error {
	current, err := s.GetApplicationRoute(ctx, route.AppID)
	if err != nil {
		return err
	}
	if current.RequestedDeploymentID != route.RequestedDeploymentID || current.ProjectID != route.ProjectID || current.ServerID != route.ServerID || current.Hostname != route.Hostname {
		return ErrRouteConflict
	}
	if current.DeploymentID == current.RequestedDeploymentID && route.DeploymentID != route.RequestedDeploymentID {
		return ErrRouteConflict
	}
	// Replayed runtime receipts must not erase a newer public verification.
	if current.DeploymentID != "" && current.DeploymentID == route.DeploymentID && current.Destination == route.Destination && current.State == "active" {
		return nil
	}
	return s.SaveApplicationRouteObservation(ctx, current, route)
}

// A public probe must not overwrite a route promotion or newer reservation
// that completed while its DNS/TLS request was in flight.
func (s *SQLStore) SaveApplicationRouteObservation(ctx context.Context, before, after core.ApplicationRoute) error {
	if before.AppID != after.AppID || before.RequestedDeploymentID != after.RequestedDeploymentID || before.ProjectID != after.ProjectID || before.ServerID != after.ServerID || before.Hostname != after.Hostname {
		return ErrRouteConflict
	}
	after.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, s.q(`UPDATE application_routes SET record=? WHERE app_id=? AND requested_deployment_id=? AND record=?`), jsonText(after), before.AppID, before.RequestedDeploymentID, jsonText(before))
	err = changed(result, err)
	if errors.Is(err, ErrNotFound) {
		return ErrRouteConflict
	}
	return err
}
func (s *SQLStore) GetApplicationRoute(ctx context.Context, appID string) (core.ApplicationRoute, error) {
	var route core.ApplicationRoute
	var raw string
	err := s.db.QueryRowContext(ctx, s.q(`SELECT record FROM application_routes WHERE app_id=?`), appID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return route, ErrNotFound
	}
	if err != nil {
		return route, err
	}
	err = json.Unmarshal([]byte(raw), &route)
	return route, err
}
func (s *SQLStore) ListApplicationRoutes(ctx context.Context) ([]core.ApplicationRoute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT record FROM application_routes ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []core.ApplicationRoute{}
	for rows.Next() {
		var route core.ApplicationRoute
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &route); err != nil {
			return nil, err
		}
		result = append(result, route)
	}
	return result, rows.Err()
}
func (s *SQLStore) DeleteApplicationRoute(ctx context.Context, appID, serverID string) error {
	_, err := s.db.ExecContext(ctx, s.q(`DELETE FROM application_routes WHERE app_id=? AND server_id=?`), appID, serverID)
	return err
}

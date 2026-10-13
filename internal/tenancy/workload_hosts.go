package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const ConnectedInstanceHostLimit = 10000

var ErrWorkloadHostLimit = errors.New("instance hostname limit reached")

// WorkloadHost contains routing ownership only. Destinations, deployment inputs
// and credentials remain in the tenant's operational database.
type WorkloadHost struct {
	Hostname  string
	TenantID  string
	AppID     string
	ProjectID string
	ServerID  string
}

func connectedHostSizeAllowed(host WorkloadHost) bool {
	return len(host.Hostname) <= 253 && len(host.AppID) <= 256 && len(host.ProjectID) <= 256 && len(host.ServerID) <= 256
}

func (c *Catalog) ReserveWorkloadHost(ctx context.Context, host WorkloadHost) error {
	return c.reserveWorkloadHost(ctx, host, ConnectedInstanceHostLimit)
}

func (c *Catalog) reserveWorkloadHost(ctx context.Context, host WorkloadHost, limit int) error {
	host.Hostname = strings.ToLower(strings.TrimSuffix(host.Hostname, "."))
	if host.Hostname == "" || strings.ContainsAny(host.Hostname, "/ :\r\n*") || host.TenantID == "" || host.AppID == "" || host.ProjectID == "" || host.ServerID == "" || limit < 1 {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A row lock serializes reservations for this installation on PostgreSQL.
	// SQLite catalogs have one writer connection. Count and insert share the
	// transaction, so concurrent requests cannot exceed the connected limit.
	var mode string
	err = tx.QueryRowContext(ctx, c.q(c.locking(`SELECT mode FROM instance_connections WHERE tenant_id=?`)), host.TenantID).Scan(&mode)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if mode == BackendConnected && !connectedHostSizeAllowed(host) {
		return ErrInvalid
	}
	var saved WorkloadHost
	err = tx.QueryRowContext(ctx, c.q(`SELECT hostname,tenant_id,app_id,project_id,server_id FROM tenant_workload_hosts WHERE hostname=?`), host.Hostname).Scan(&saved.Hostname, &saved.TenantID, &saved.AppID, &saved.ProjectID, &saved.ServerID)
	if err == nil {
		if saved != host {
			return ErrDenied
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if mode == BackendConnected {
		var count int
		if err = tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*) FROM tenant_workload_hosts WHERE tenant_id=?`), host.TenantID).Scan(&count); err != nil {
			return err
		}
		if count >= limit {
			return ErrWorkloadHostLimit
		}
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_workload_hosts(hostname,tenant_id,app_id,project_id,server_id) VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`), host.Hostname, host.TenantID, host.AppID, host.ProjectID, host.ServerID)
	if err != nil {
		return conflict(err)
	}
	err = tx.QueryRowContext(ctx, c.q(`SELECT hostname,tenant_id,app_id,project_id,server_id FROM tenant_workload_hosts WHERE hostname=?`), host.Hostname).Scan(&saved.Hostname, &saved.TenantID, &saved.AppID, &saved.ProjectID, &saved.ServerID)
	if err != nil {
		return missing(err)
	}
	if saved != host {
		return ErrDenied
	}
	return tx.Commit()
}

func (c *Catalog) WorkloadHost(ctx context.Context, hostname string) (WorkloadHost, error) {
	var host WorkloadHost
	err := c.db.QueryRowContext(ctx, c.q(`SELECT hostname,tenant_id,app_id,project_id,server_id FROM tenant_workload_hosts WHERE hostname=?`), strings.ToLower(strings.TrimSuffix(hostname, "."))).Scan(&host.Hostname, &host.TenantID, &host.AppID, &host.ProjectID, &host.ServerID)
	return host, missing(err)
}

func (c *Catalog) ReleaseWorkloadHost(ctx context.Context, tenant, app, server string) error {
	_, err := c.db.ExecContext(ctx, c.q(`DELETE FROM tenant_workload_hosts WHERE tenant_id=? AND app_id=? AND server_id=?`), tenant, app, server)
	return err
}

// InstanceWorkloadHosts lists only the routing ownership for one installation.
func (c *Catalog) InstanceWorkloadHosts(ctx context.Context, tenant string) ([]WorkloadHost, error) {
	return c.instanceWorkloadHosts(ctx, tenant, ConnectedInstanceHostLimit)
}

func (c *Catalog) instanceWorkloadHosts(ctx context.Context, tenant string, limit int) ([]WorkloadHost, error) {
	if limit < 1 {
		return nil, ErrInvalid
	}
	connection, err := c.InstanceConnection(ctx, tenant)
	if err != nil {
		return nil, err
	}
	query := `SELECT hostname,tenant_id,app_id,project_id,server_id FROM tenant_workload_hosts WHERE tenant_id=? ORDER BY hostname`
	args := []any{tenant}
	if connection.BackendMode == BackendConnected {
		query += ` LIMIT ?`
		args = append(args, limit+1)
	}
	rows, err := c.db.QueryContext(ctx, c.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WorkloadHost{}
	for rows.Next() {
		var v WorkloadHost
		if err = rows.Scan(&v.Hostname, &v.TenantID, &v.AppID, &v.ProjectID, &v.ServerID); err != nil {
			return nil, err
		}
		if connection.BackendMode == BackendConnected && !connectedHostSizeAllowed(v) {
			return nil, ErrInvalid
		}
		out = append(out, v)
		if connection.BackendMode == BackendConnected && len(out) > limit {
			return nil, ErrWorkloadHostLimit
		}
	}
	return out, rows.Err()
}

package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// DeleteConnectedInstance removes control-plane metadata only. It never opens
// the installation's operational database or contacts its server.
func (c *Catalog) DeleteConnectedInstance(ctx context.Context, actor, id, slug, externalID string) (InstanceConnection, error) {
	return c.deleteInstance(ctx, actor, id, slug, externalID, false)
}

// DeleteInactiveInstance also removes older metadata when the hosted server has
// disabled embedded backends. The caller must ensure no runtime is running.
func (c *Catalog) DeleteInactiveInstance(ctx context.Context, actor, id, slug, externalID string) (InstanceConnection, error) {
	return c.deleteInstance(ctx, actor, id, slug, externalID, true)
}

func (c *Catalog) deleteInstance(ctx context.Context, actor, id, slug, externalID string, allowInactive bool) (InstanceConnection, error) {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return InstanceConnection{}, err
	}
	defer tx.Rollback()
	if err = c.membershipWrite(ctx, tx, actor, id); err != nil {
		return InstanceConnection{}, err
	}
	tenant, err := scanTenant(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT id,slug,name,state,created_at FROM tenants WHERE id=?`)), id))
	if err != nil {
		return InstanceConnection{}, err
	}
	if slug != tenant.Slug {
		return InstanceConnection{}, ErrInvalid
	}
	// Old webhook senders may still use this address after deletion. Keep its
	// name with its owners so another account cannot receive those requests.
	if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO instance_name_reservations(slug,user_id) SELECT ?,user_id FROM tenant_memberships WHERE tenant_id=? AND role='owner' AND state='active' ON CONFLICT DO NOTHING`), tenant.Slug, id); err != nil {
		return InstanceConnection{}, err
	}
	connection, err := scanConnection(tx.QueryRowContext(ctx, c.q(c.locking(`SELECT `+connectionColumns+` FROM instance_connections WHERE tenant_id=?`)), id))
	if errors.Is(err, ErrNotFound) {
		connection = InstanceConnection{TenantID: id, BackendMode: BackendEmbedded}
	} else if err != nil {
		return connection, err
	}
	if connection.BackendMode == BackendConnected {
		connection, err = c.revokeInstance(ctx, tx, id, -1)
	} else if allowInactive {
		var metadata []byte
		metadata, err = json.Marshal(tenant)
		if err == nil {
			_, err = tx.ExecContext(ctx, c.q(`INSERT INTO retired_instance_data(tenant_id,metadata_json,owner_id,retired_at) VALUES(?,?,?,?)`), id, string(metadata), actor, millis(c.now()))
		}
		if err == nil {
			err = c.revokeInstanceTokens(ctx, tx, id)
		}
	} else {
		return connection, ErrDenied
	}
	if err != nil {
		return connection, err
	}
	var organization string
	err = tx.QueryRowContext(ctx, c.q(`SELECT organization_id FROM tenant_workos_organizations WHERE tenant_id=?`), id).Scan(&organization)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return connection, err
	}
	// Keep provider cleanup durable even if WorkOS is down or organization
	// creation previously succeeded before its catalog binding was saved.
	if organization != "" || externalID != "" {
		if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO instance_organization_deletions(tenant_id,organization_id,external_id) VALUES(?,?,?)`), id, organization, externalID); err != nil {
			return connection, err
		}
	}
	if err = c.deleteInstanceDNS(ctx, tx, id); err != nil {
		return connection, err
	}
	for _, table := range []string{"tenant_handoffs", "instance_enrollments", "tenant_workload_hosts", "tenant_domains", "tenant_provisioning", "tenant_usage", "tenant_join_requests", "tenant_invitations", "tenant_workos_organizations", "tenant_memberships", "instance_connections"} {
		if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM `+table+` WHERE tenant_id=?`), id); err != nil {
			return connection, err
		}
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenants WHERE id=?`), id); err != nil {
		return connection, err
	}
	return connection, tx.Commit()
}

func (c *Catalog) deleteInstanceDNS(ctx context.Context, tx *sql.Tx, id string) error {
	var generation int64
	if err := tx.QueryRowContext(ctx, c.locking(`SELECT generation FROM tenant_zone_generation WHERE id=1`)).Scan(&generation); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, c.q(`SELECT `+zoneColumns+` FROM tenant_zone_records WHERE tenant_id=?`), id)
	if err != nil {
		return err
	}
	var records []ZoneRecord
	for rows.Next() {
		record, err := scanZone(rows)
		if err != nil {
			rows.Close()
			return err
		}
		records = append(records, record)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, record := range records {
		generation++
		if err = c.enqueueDNSChange(ctx, tx, DNSChange{Record: record, Generation: uint64(generation), Delete: true}); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenant_zone_records WHERE tenant_id=?`), id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, c.q(`UPDATE tenant_zone_generation SET generation=? WHERE id=1`), generation)
	return err
}

type OrganizationDeletion struct {
	TenantID       string
	OrganizationID string
	ExternalID     string
}

func (c *Catalog) PendingOrganizationDeletions(ctx context.Context) ([]OrganizationDeletion, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT tenant_id,organization_id,external_id FROM instance_organization_deletions ORDER BY tenant_id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []OrganizationDeletion{}
	for rows.Next() {
		var v OrganizationDeletion
		if err = rows.Scan(&v.TenantID, &v.OrganizationID, &v.ExternalID); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (c *Catalog) CompleteOrganizationDeletion(ctx context.Context, id string) error {
	_, err := c.db.ExecContext(ctx, c.q(`DELETE FROM instance_organization_deletions WHERE tenant_id=?`), id)
	return err
}

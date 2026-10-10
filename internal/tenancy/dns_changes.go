package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
)

const dnsChangeColumns = `id,owner_id,tenant_id,name,type,values_json,ttl,record_generation,generation,delete_record`

// initializeDNSChanges runs once for catalogs created before publication was
// tracked. The marker and initial intents commit together with the migration.
func (c *Catalog) initializeDNSChanges(ctx context.Context, tx *sql.Tx) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO tenancy_schema(version) VALUES(2) ON CONFLICT(version) DO NOTHING`)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 0 {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tenant_dns_changes(`+dnsChangeColumns+`) SELECT id,owner_id,tenant_id,name,type,values_json,ttl,generation,generation,0 FROM tenant_zone_records WHERE id<>'platform-zone-config' ON CONFLICT(id) DO NOTHING`)
	return err
}

func (c *Catalog) enqueueDNSChange(ctx context.Context, tx *sql.Tx, change DNSChange) error {
	r := change.Record
	if r.ID == "platform-zone-config" {
		return nil
	}
	values, err := json.Marshal(r.Values)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_dns_changes(`+dnsChangeColumns+`) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET owner_id=excluded.owner_id,tenant_id=excluded.tenant_id,name=excluded.name,type=excluded.type,values_json=excluded.values_json,ttl=excluded.ttl,record_generation=excluded.record_generation,generation=excluded.generation,delete_record=excluded.delete_record`), r.ID, r.OwnerID, r.TenantID, r.Name, r.Type, string(values), int64(r.TTL), int64(r.Generation), int64(change.Generation), boolInt(change.Delete))
	return err
}

// PendingDNSChanges returns publication work without removing it. Failed or
// interrupted provider calls leave the same change available for a retry.
func (c *Catalog) PendingDNSChanges(ctx context.Context, limit int) ([]DNSChange, error) {
	return c.pendingDNSChanges(ctx, "", limit)
}

func (c *Catalog) PendingTenantDNSChanges(ctx context.Context, tenantID string, limit int) ([]DNSChange, error) {
	if tenantID == "" {
		return nil, ErrInvalid
	}
	return c.pendingDNSChanges(ctx, tenantID, limit)
}

func (c *Catalog) pendingDNSChanges(ctx context.Context, tenantID string, limit int) ([]DNSChange, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	query := `SELECT ` + dnsChangeColumns + ` FROM tenant_dns_changes`
	args := []any{}
	if tenantID != "" {
		query += ` WHERE tenant_id=?`
		args = append(args, tenantID)
	}
	query += ` ORDER BY generation,id LIMIT ?`
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, c.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	changes := []DNSChange{}
	for rows.Next() {
		change, err := scanDNSChange(rows)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, rows.Err()
}

func (c *Catalog) DNSChange(ctx context.Context, id string) (DNSChange, error) {
	if id == "" {
		return DNSChange{}, ErrInvalid
	}
	return scanDNSChange(c.db.QueryRowContext(ctx, c.q(`SELECT `+dnsChangeColumns+` FROM tenant_dns_changes WHERE id=?`), id))
}

func scanDNSChange(row scanner) (DNSChange, error) {
	var change DNSChange
	r := &change.Record
	var values string
	var ttl, recordGeneration, generation int64
	var deleted int
	if err := row.Scan(&r.ID, &r.OwnerID, &r.TenantID, &r.Name, &r.Type, &values, &ttl, &recordGeneration, &generation, &deleted); err != nil {
		return change, missing(err)
	}
	if err := json.Unmarshal([]byte(values), &r.Values); err != nil {
		return change, err
	}
	r.TTL, r.Generation = uint32(ttl), uint64(recordGeneration)
	change.Generation, change.Delete = uint64(generation), deleted != 0
	return change, nil
}

// CompleteDNSChange acknowledges only the published generation. A concurrent
// update or recreation remains pending even if an older provider call succeeds.
func (c *Catalog) CompleteDNSChange(ctx context.Context, id string, generation uint64) error {
	if id == "" || generation == 0 || generation > math.MaxInt64 {
		return ErrInvalid
	}
	_, err := c.db.ExecContext(ctx, c.q(`DELETE FROM tenant_dns_changes WHERE id=? AND generation=?`), id, int64(generation))
	return err
}

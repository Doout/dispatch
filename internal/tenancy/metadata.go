package tenancy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

func (c *Catalog) SaveUsage(ctx context.Context, u Usage) error {
	if u.TenantID == "" || u.PeriodStart.IsZero() || !u.PeriodEnd.After(u.PeriodStart) {
		return ErrInvalid
	}
	valid := map[string]int64{"projects": u.Projects, "applications": u.Applications, "members": u.Members, "builds": u.Builds, "deployments": u.Deployments, "buildSeconds": u.BuildSeconds, "runtimeSeconds": u.RuntimeSeconds, "storageBytes": u.StorageBytes, "transferBytes": u.TransferBytes}
	for _, v := range valid {
		if v < 0 {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, field := range u.Measured {
		if _, ok := valid[field]; !ok || seen[field] {
			return ErrInvalid
		}
		seen[field] = true
	}
	sort.Strings(u.Measured)
	if u.Measured == nil {
		u.Measured = []string{}
	}
	measured, _ := json.Marshal(u.Measured)
	_, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_usage(tenant_id,period_start,period_end,projects,applications,members,builds,deployments,build_seconds,runtime_seconds,storage_bytes,transfer_bytes,updated_at,measured) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(tenant_id,period_start,period_end) DO UPDATE SET projects=excluded.projects,applications=excluded.applications,members=excluded.members,builds=excluded.builds,deployments=excluded.deployments,build_seconds=excluded.build_seconds,runtime_seconds=excluded.runtime_seconds,storage_bytes=excluded.storage_bytes,transfer_bytes=excluded.transfer_bytes,updated_at=excluded.updated_at,measured=excluded.measured`), u.TenantID, millis(u.PeriodStart), millis(u.PeriodEnd), u.Projects, u.Applications, u.Members, u.Builds, u.Deployments, u.BuildSeconds, u.RuntimeSeconds, u.StorageBytes, u.TransferBytes, millis(c.now()), string(measured))
	return err
}
func (c *Catalog) TenantUsage(ctx context.Context, tenant string, start, end time.Time) ([]Usage, error) {
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT tenant_id,period_start,period_end,projects,applications,members,builds,deployments,build_seconds,runtime_seconds,storage_bytes,transfer_bytes,updated_at,measured FROM tenant_usage WHERE tenant_id=? AND period_start>=? AND period_start<? ORDER BY period_start`), tenant, millis(start), millis(end))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Usage{}
	for rows.Next() {
		var u Usage
		var a, b, updated int64
		var measured string
		if err = rows.Scan(&u.TenantID, &a, &b, &u.Projects, &u.Applications, &u.Members, &u.Builds, &u.Deployments, &u.BuildSeconds, &u.RuntimeSeconds, &u.StorageBytes, &u.TransferBytes, &updated, &measured); err != nil {
			return nil, err
		}
		u.PeriodStart, u.PeriodEnd, u.UpdatedAt = instant(a), instant(b), instant(updated)
		if err = json.Unmarshal([]byte(measured), &u.Measured); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}
func (c *Catalog) SaveProvisioningJob(ctx context.Context, p ProvisioningJob) error {
	if p.TenantID == "" || (p.State != "pending" && p.State != "running" && p.State != "succeeded" && p.State != "failed") || len(p.Phase) > 80 {
		return ErrInvalid
	}
	_, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_provisioning(tenant_id,state,phase,updated_at) VALUES(?,?,?,?) ON CONFLICT(tenant_id) DO UPDATE SET state=excluded.state,phase=excluded.phase,updated_at=excluded.updated_at`), p.TenantID, p.State, p.Phase, millis(c.now()))
	return err
}
func (c *Catalog) ProvisioningJob(ctx context.Context, tenant string) (ProvisioningJob, error) {
	var p ProvisioningJob
	var updated int64
	err := c.db.QueryRowContext(ctx, c.q(`SELECT tenant_id,state,phase,updated_at FROM tenant_provisioning WHERE tenant_id=?`), tenant).Scan(&p.TenantID, &p.State, &p.Phase, &updated)
	p.UpdatedAt = instant(updated)
	return p, missing(err)
}

func (c *Catalog) SaveDomain(ctx context.Context, d Domain) error {
	d.Hostname = strings.ToLower(strings.TrimSuffix(d.Hostname, "."))
	if d.ID == "" || d.TenantID == "" || d.Hostname == "" || strings.ContainsAny(d.Hostname, "/ :\r\n") {
		return ErrInvalid
	}
	res, err := c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_domains(id,tenant_id,hostname,kind,state,claim_hash,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,claim_hash=excluded.claim_hash WHERE tenant_domains.tenant_id=excluded.tenant_id AND tenant_domains.hostname=excluded.hostname AND tenant_domains.kind=excluded.kind`), d.ID, d.TenantID, d.Hostname, d.Kind, d.State, d.ClaimHash, millis(c.now()))
	if err != nil {
		return conflict(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrDenied
	}
	return nil
}
func (c *Catalog) DomainByHostname(ctx context.Context, host string) (Domain, error) {
	var d Domain
	var created int64
	err := c.db.QueryRowContext(ctx, c.q(`SELECT id,tenant_id,hostname,kind,state,claim_hash,created_at FROM tenant_domains WHERE hostname=?`), strings.ToLower(strings.TrimSuffix(host, "."))).Scan(&d.ID, &d.TenantID, &d.Hostname, &d.Kind, &d.State, &d.ClaimHash, &created)
	d.CreatedAt = instant(created)
	return d, missing(err)
}
func (c *Catalog) Domains(ctx context.Context, tenant string) ([]Domain, error) {
	rows, err := c.db.QueryContext(ctx, c.q(`SELECT id,tenant_id,hostname,kind,state,claim_hash,created_at FROM tenant_domains WHERE tenant_id=? ORDER BY hostname`), tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		var created int64
		if err = rows.Scan(&d.ID, &d.TenantID, &d.Hostname, &d.Kind, &d.State, &d.ClaimHash, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = instant(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

func scanZone(row scanner) (ZoneRecord, error) {
	var r ZoneRecord
	var values string
	var ttl, generation int64
	err := row.Scan(&r.ID, &r.OwnerID, &r.TenantID, &r.Name, &r.Type, &values, &ttl, &generation)
	if err != nil {
		return r, missing(err)
	}
	r.TTL, r.Generation = uint32(ttl), uint64(generation)
	err = json.Unmarshal([]byte(values), &r.Values)
	return r, err
}

const zoneColumns = `id,owner_id,tenant_id,name,type,values_json,ttl,generation`

// SaveZoneRecord uses Generation as the expected existing record version. A new
// record must use zero; the returned version is assigned by the catalog.
func (c *Catalog) SaveZoneRecord(ctx context.Context, r ZoneRecord) (ZoneRecord, error) {
	r.Name = strings.ToLower(strings.TrimSuffix(r.Name, "."))
	r.Type = strings.ToUpper(r.Type)
	if r.ID == "" || r.OwnerID == "" || r.TenantID == "" || r.Name == "" || r.Type == "" || len(r.Values) == 0 || len(r.Values) > 100 || r.TTL == 0 {
		return ZoneRecord{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return ZoneRecord{}, err
	}
	defer tx.Rollback()
	var generation int64
	if err = tx.QueryRowContext(ctx, c.locking(`SELECT generation FROM tenant_zone_generation WHERE id=1`)).Scan(&generation); err != nil {
		return ZoneRecord{}, err
	}
	var aliases int
	if err = tx.QueryRowContext(ctx, c.q(`SELECT COUNT(*) FROM tenant_zone_records WHERE name=? AND id<>? AND (type='CNAME' OR ?='CNAME')`), r.Name, r.ID, r.Type).Scan(&aliases); err != nil {
		return ZoneRecord{}, err
	}
	if aliases > 0 {
		return ZoneRecord{}, ErrConflict
	}
	existing, err := scanZone(tx.QueryRowContext(ctx, c.q(`SELECT `+zoneColumns+` FROM tenant_zone_records WHERE id=?`), r.ID))
	if errors.Is(err, ErrNotFound) {
		if r.Generation != 0 {
			return ZoneRecord{}, ErrConflict
		}
		pending, pendingErr := scanDNSChange(tx.QueryRowContext(ctx, c.q(`SELECT `+dnsChangeColumns+` FROM tenant_dns_changes WHERE id=?`), r.ID))
		if pendingErr != nil && !errors.Is(pendingErr, ErrNotFound) {
			return ZoneRecord{}, pendingErr
		}
		if pendingErr == nil && pending.Delete && (pending.Record.OwnerID != r.OwnerID || pending.Record.TenantID != r.TenantID || pending.Record.Name != r.Name || pending.Record.Type != r.Type) {
			return ZoneRecord{}, ErrDenied
		}
	} else if err != nil {
		return ZoneRecord{}, err
	} else {
		if existing.OwnerID != r.OwnerID || existing.TenantID != r.TenantID || existing.Name != r.Name || existing.Type != r.Type {
			return ZoneRecord{}, ErrDenied
		}
		if existing.Generation != r.Generation {
			return ZoneRecord{}, ErrConflict
		}
	}
	generation++
	r.Generation = uint64(generation)
	values, err := json.Marshal(r.Values)
	if err != nil {
		return ZoneRecord{}, err
	}
	_, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_zone_records(`+zoneColumns+`) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,values_json=excluded.values_json,ttl=excluded.ttl,generation=excluded.generation`), r.ID, r.OwnerID, r.TenantID, r.Name, r.Type, string(values), int64(r.TTL), generation)
	if err != nil {
		return ZoneRecord{}, err
	}
	if _, err = tx.ExecContext(ctx, c.q(`UPDATE tenant_zone_generation SET generation=? WHERE id=1`), generation); err != nil {
		return ZoneRecord{}, err
	}
	if err = c.enqueueDNSChange(ctx, tx, DNSChange{Record: r, Generation: r.Generation}); err != nil {
		return ZoneRecord{}, err
	}
	if err = tx.Commit(); err != nil {
		return ZoneRecord{}, err
	}
	return r, nil
}
func (c *Catalog) DeleteZoneRecord(ctx context.Context, id, owner string, expected uint64) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var generation int64
	if err = tx.QueryRowContext(ctx, c.locking(`SELECT generation FROM tenant_zone_generation WHERE id=1`)).Scan(&generation); err != nil {
		return err
	}
	r, err := scanZone(tx.QueryRowContext(ctx, c.q(`SELECT `+zoneColumns+` FROM tenant_zone_records WHERE id=?`), id))
	if err != nil {
		return err
	}
	if r.OwnerID != owner {
		return ErrDenied
	}
	if r.Generation != expected {
		return ErrConflict
	}
	if _, err = tx.ExecContext(ctx, c.q(`DELETE FROM tenant_zone_records WHERE id=?`), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, c.q(`UPDATE tenant_zone_generation SET generation=? WHERE id=1`), generation+1); err != nil {
		return err
	}
	if err = c.enqueueDNSChange(ctx, tx, DNSChange{Record: r, Generation: uint64(generation + 1), Delete: true}); err != nil {
		return err
	}
	return tx.Commit()
}
func (c *Catalog) ZoneRecords(ctx context.Context) (uint64, []ZoneRecord, error) {
	opts := &sql.TxOptions{ReadOnly: true}
	if c.postgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := c.db.BeginTx(ctx, opts)
	if err != nil {
		return 0, nil, err
	}
	defer tx.Rollback()
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT generation FROM tenant_zone_generation WHERE id=1`).Scan(&generation); err != nil {
		return 0, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+zoneColumns+` FROM tenant_zone_records ORDER BY id`)
	if err != nil {
		return 0, nil, err
	}
	out := []ZoneRecord{}
	for rows.Next() {
		r, err := scanZone(rows)
		if err != nil {
			rows.Close()
			return 0, nil, err
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, nil, err
	}
	if err = tx.Commit(); err != nil {
		return 0, nil, err
	}
	return uint64(generation), out, nil
}

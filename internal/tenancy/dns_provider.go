package tenancy

import (
	"context"
	"strings"
)

// BindDNSProvider fixes the publication destination independently of provider
// credentials. Changing destinations requires an explicit DNS migration.
func (c *Catalog) BindDNSProvider(ctx context.Context, target string) error {
	if target == "" || len(target) > 256 || strings.TrimSpace(target) != target || strings.ContainsAny(target, "\x00\r\n") {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, c.q(`INSERT INTO tenant_dns_provider(slot,target) VALUES(1,?) ON CONFLICT(slot) DO NOTHING`), target); err != nil {
		return err
	}
	var bound string
	if err = tx.QueryRowContext(ctx, `SELECT target FROM tenant_dns_provider WHERE slot=1`).Scan(&bound); err != nil {
		return err
	}
	if bound != target {
		return ErrConflict
	}
	return tx.Commit()
}

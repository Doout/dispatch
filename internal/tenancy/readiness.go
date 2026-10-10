package tenancy

import "context"

// Ready checks the catalog connection and schema without opening tenant stores.
func (c *Catalog) Ready(ctx context.Context) error {
	var version int
	return c.db.QueryRowContext(ctx, `SELECT version FROM tenancy_schema WHERE version=1`).Scan(&version)
}

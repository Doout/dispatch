package store

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// Each fixture gets its own schema, including tests that reopen their store.
// A shared disposable database can therefore run the whole persistence suite
// repeatedly without fixed fixture IDs or demo records leaking between tests.
func isolatedPostgresURL(t *testing.T, variable string) string {
	t.Helper()
	dsn := os.Getenv(variable)
	if dsn == "" {
		dsn = os.Getenv("DISPATCH_STORE_POSTGRES_URL")
	}
	if dsn == "" {
		return ""
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatal("PostgreSQL fixtures require a postgres:// or postgresql:// URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal("open disposable PostgreSQL fixture database:", err)
	}
	schema := "dispatch_test_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		admin.Close()
		t.Fatal("create isolated fixture schema:", err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		defer admin.Close()
		if _, err := admin.db.ExecContext(cleanup, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error("remove isolated fixture schema:", err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

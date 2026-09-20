package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestDeploymentCatalogPersistence(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "catalog.db")
			if backend == "postgres" {
				dsn = os.Getenv("DISPATCH_HISTORY_POSTGRES_URL")
				if dsn == "" {
					t.Skip("set DISPATCH_HISTORY_POSTGRES_URL to a disposable database")
				}
			}
			ctx := context.Background()
			data, err := Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer data.Close()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			must(data.Migrate(ctx))
			must(data.SeedDemo(ctx))
			apps, err := data.ListApps(ctx)
			must(err)
			now := time.Now().UTC().Add(24 * time.Hour)
			for _, id := range []string{"catalog-store-a", "catalog-store-b", "catalog-store-c"} {
				must(data.CreateDeployment(ctx, core.Deployment{ID: id, AppID: apps[0].ID, CommitSHA: "literal_%_revision", State: core.DeploymentFailed, CreatedAt: now, Snapshot: core.DeploymentSnapshot{TargetID: "target", Values: map[string]any{"password": "secret"}}}))
			}
			page, err := data.SearchDeploymentHistory(ctx, core.DeploymentSearch{AppIDs: []string{apps[0].ID}, Query: "_%", State: "failed", Limit: 2})
			must(err)
			if len(page) != 2 || page[0].ID != "catalog-store-c" || page[1].ID != "catalog-store-b" || page[0].Snapshot.TargetID != "" {
				t.Fatalf("incorrect bounded sanitized search: %+v", page)
			}
			next, err := data.SearchDeploymentHistory(ctx, core.DeploymentSearch{AppIDs: []string{apps[0].ID}, Query: "_%", State: "failed", Before: page[1].ID, Limit: 2})
			must(err)
			if len(next) != 1 || next[0].ID != "catalog-store-a" {
				t.Fatal("pagination skipped or repeated same-time records")
			}
			empty, err := data.SearchDeploymentHistory(ctx, core.DeploymentSearch{AppIDs: []string{"inaccessible"}, Query: "_%"})
			must(err)
			if len(empty) != 0 {
				t.Fatal("search escaped authorized app set")
			}
			empty, err = data.SearchDeploymentHistory(ctx, core.DeploymentSearch{Query: "_%"})
			must(err)
			if len(empty) != 0 {
				t.Fatal("empty authorization searched all history")
			}
			empty, err = data.SearchDeploymentHistory(ctx, core.DeploymentSearch{AppIDs: []string{apps[0].ID}, Query: "' OR 1=1 --"})
			must(err)
			if len(empty) != 0 {
				t.Fatal("query interpreted as SQL")
			}
		})
	}
}

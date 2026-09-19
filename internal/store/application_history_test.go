package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestApplicationHistoryPersistence(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "history.db")
			if backend == "postgres" {
				dsn = os.Getenv("DISPATCH_HISTORY_POSTGRES_URL")
				if dsn == "" {
					t.Skip("set DISPATCH_HISTORY_POSTGRES_URL to an empty disposable database")
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
			now := time.Now().UTC().Add(time.Hour)
			for _, id := range []string{"history-a", "history-b", "history-c"} {
				must(data.CreateDeployment(ctx, core.Deployment{ID: id, AppID: apps[0].ID, CreatedAt: now, State: core.DeploymentSucceeded, Snapshot: core.DeploymentSnapshot{TargetID: "original-target", Values: map[string]any{"replicas": 3}}}))
			}
			first, err := data.ListApplicationHistory(ctx, apps[0].ID, "", 2)
			must(err)
			if len(first) != 2 || first[0].ID != "history-c" || first[1].ID != "history-b" || first[0].Snapshot.TargetID != "" || first[0].App != nil {
				t.Fatal("incorrect history order or hydrated snapshot")
			}
			page, err := data.ListApplicationHistory(ctx, apps[0].ID, first[1].ID, 2)
			must(err)
			if len(page) == 0 || page[0].ID != "history-a" {
				t.Fatal("unstable page cursor")
			}
			other, err := data.ListApplicationHistory(ctx, "absent-app", "", 2)
			must(err)
			if len(other) != 0 {
				t.Fatal("history crossed application boundary")
			}
			saved, err := data.GetDeployment(ctx, "history-a")
			must(err)
			if saved.Snapshot.TargetID != "original-target" {
				t.Fatal("lost comparison snapshot")
			}
		})
	}
}

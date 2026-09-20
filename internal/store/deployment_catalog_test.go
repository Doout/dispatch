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

func TestDeploymentCompletionWindow(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "completion.db")
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
			from := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
			to := from.Add(24 * time.Hour)
			before := from.Add(-time.Nanosecond)
			fraction := from.Add(500 * time.Nanosecond)
			middle := from.Add(500 * time.Millisecond)
			last := to.Add(-time.Nanosecond)
			cases := []struct {
				id       string
				created  time.Time
				finished *time.Time
				state    core.DeploymentState
				appID    string
			}{
				{"before", from.Add(time.Hour), &before, core.DeploymentSucceeded, apps[0].ID},
				{"start", from.Add(-2 * time.Hour), &from, core.DeploymentSucceeded, apps[0].ID},
				{"fraction", from.Add(-5 * time.Hour), &fraction, core.DeploymentSucceeded, apps[0].ID},
				{"middle", from.Add(-4 * time.Hour), &middle, core.DeploymentFailed, apps[0].ID},
				{"last", from.Add(-3 * time.Hour), &last, core.DeploymentCancelled, apps[0].ID},
				{"end", from.Add(-time.Hour), &to, core.DeploymentSucceeded, apps[0].ID},
				{"legacy", from.Add(time.Second), nil, core.DeploymentSucceeded, apps[0].ID},
				{"active", from.Add(2 * time.Second), nil, core.DeploymentQueued, apps[0].ID},
				{"private", from.Add(3 * time.Second), &middle, core.DeploymentSucceeded, apps[1].ID},
			}
			for _, c := range cases {
				must(data.CreateDeployment(ctx, core.Deployment{ID: "completion-" + c.id, AppID: c.appID, CommitSHA: "completion-window", State: c.state, CreatedAt: c.created, FinishedAt: c.finished}))
			}
			filter := core.DeploymentSearch{AppIDs: []string{apps[0].ID}, Revision: "completion-window", CompletedFrom: &from, CompletedTo: &to, Limit: 2}
			expected := map[string]bool{"completion-start": true, "completion-fraction": true, "completion-middle": true, "completion-last": true, "completion-legacy": true}
			seen := map[string]bool{}
			for {
				page, err := data.SearchDeploymentHistory(ctx, filter)
				must(err)
				if len(page) == 0 {
					break
				}
				for _, d := range page {
					if !expected[d.ID] || seen[d.ID] {
						t.Fatalf("wrong completion page entry %s", d.ID)
					}
					seen[d.ID] = true
				}
				filter.Before = page[len(page)-1].ID
			}
			if len(seen) != len(expected) {
				t.Fatalf("missing completed runs: %v", seen)
			}
			lower := from.Add(100 * time.Nanosecond)
			upper := from.Add(time.Microsecond)
			filter.Before = ""
			filter.CompletedFrom = &lower
			filter.CompletedTo = &upper
			narrow, err := data.SearchDeploymentHistory(ctx, filter)
			must(err)
			if len(narrow) != 1 || narrow[0].ID != "completion-fraction" {
				t.Fatalf("fractional UTC range lost precision: %+v", narrow)
			}
		})
	}
}

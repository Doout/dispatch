package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestAnalyticsOutboxAtomicCompletionAndExactAck(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "analytics.db"))
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
	old, err := data.ListAnalyticsEvents(ctx, 500)
	must(err)
	must(data.AckAnalyticsEvents(ctx, old))
	apps, err := data.ListApps(ctx)
	must(err)
	now := time.Now().UTC()
	item := core.Deployment{ID: "export-me", AppID: apps[0].ID, State: core.DeploymentBuilding, CreatedAt: now}
	must(data.CreateDeployment(ctx, item))
	events, err := data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(events) != 0 {
		t.Fatal("exported unfinished run")
	}
	// The completion and queue record must commit or roll back together.
	tx, err := data.db.BeginTx(ctx, nil)
	must(err)
	_, err = tx.ExecContext(ctx, "UPDATE deployments SET state='succeeded',finished_at=? WHERE id=?", stamp(now), "export-me")
	must(err)
	must(tx.Rollback())
	events, err = data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(events) != 0 {
		t.Fatal("rolled back completion was exported")
	}
	_, err = data.db.ExecContext(ctx, "UPDATE deployments SET state='succeeded',finished_at=? WHERE id=?", stamp(now), "export-me")
	must(err)
	events, err = data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(events) != 1 || events[0].ProjectID != apps[0].ProjectID || events[0].EntityID != "export-me" {
		t.Fatalf("wrong completion: %+v", events)
	}
	first := events[0]
	_, err = data.db.ExecContext(ctx, "UPDATE deployments SET message='changed log' WHERE id='export-me'")
	must(err)
	events, err = data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(events) != 1 {
		t.Fatal("unrelated write duplicated completion")
	}
	_, err = data.db.ExecContext(ctx, "UPDATE deployments SET state='failed' WHERE id='export-me'")
	must(err)
	events, err = data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(events) != 2 {
		t.Fatal("correction was not exported")
	}
	must(data.AckAnalyticsEvents(ctx, events[1:]))
	events, err = data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(events) != 1 || events[0].ID != first.ID {
		t.Fatal("acknowledgement lost an unread event")
	}
}

func TestPostgresAnalyticsCompletionWhenConfigured(t *testing.T) {
	url := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("PostgreSQL not configured")
	}
	ctx := context.Background()
	data, err := Open(ctx, url)
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
	now := time.Now().UTC()
	id := fmt.Sprintf("pg-analytics-%d", now.UnixNano())
	must(data.CreateDeployment(ctx, core.Deployment{ID: id, AppID: apps[0].ID, State: core.DeploymentSucceeded, CreatedAt: now, FinishedAt: &now}))
	events, err := data.ListAnalyticsEvents(ctx, 500)
	must(err)
	found := false
	for _, event := range events {
		if event.EntityID == id && event.State == "succeeded" && event.ProjectID == apps[0].ProjectID {
			found = true
		}
	}
	if !found {
		t.Fatal("PostgreSQL completion trigger did not enqueue the event")
	}
}
func TestAnalyticsMigrationBackfillsExistingHistory(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "backfill.db"))
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
	for _, kind := range []string{"deployment", "workflow", "job"} {
		for _, event := range []string{"insert", "update"} {
			_, err = data.db.ExecContext(ctx, "DROP TRIGGER analytics_"+kind+"_"+event)
			must(err)
		}
	}
	_, err = data.db.ExecContext(ctx, "DROP TABLE analytics_outbox")
	must(err)
	_, err = data.db.ExecContext(ctx, "DELETE FROM schema_migrations WHERE version='038_analytics_outbox'")
	must(err)
	must(data.SeedDemo(ctx))
	expected, err := data.ListDeployments(ctx, 100)
	must(err)
	must(data.Migrate(ctx))
	events, err := data.ListAnalyticsEvents(ctx, 500)
	must(err)
	completed := 0
	for _, item := range expected {
		if item.State == core.DeploymentSucceeded || item.State == core.DeploymentFailed || item.State == core.DeploymentCancelled {
			completed++
		}
	}
	if len(events) != completed || len(events) == 0 {
		t.Fatalf("backfill got %d records for %d deployments", len(events), completed)
	}
	must(data.Migrate(ctx))
	again, err := data.ListAnalyticsEvents(ctx, 500)
	must(err)
	if len(again) != len(events) {
		t.Fatal("migration repeated the backfill")
	}
}

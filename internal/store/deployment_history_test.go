package store

import (
	"context"
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDeploymentHistoryUsesDateIndex(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Include the snapshot column: sorting wide rows caused multi-second reads.
	rows, err := data.db.QueryContext(ctx, `EXPLAIN QUERY PLAN SELECT id,app_id,commit_sha,spec_digest,state,message,created_at,started_at,finished_at,lease_until,outputs,spec_snapshot FROM deployments ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err = rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "deployments_created_at") || strings.Contains(joined, "TEMP B-TREE") {
		t.Fatalf("history sorts snapshots instead of using date index:\n%s", joined)
	}
}

func TestDeploymentSummariesPreservePublicResponse(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "summaries.db"))
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
	must(data.CreateDeployment(ctx, core.Deployment{ID: "large-snapshot", AppID: apps[0].ID, State: core.DeploymentSucceeded, CreatedAt: time.Now().Add(time.Hour), Snapshot: core.DeploymentSnapshot{Chart: "chart", Values: map[string]any{"payload": strings.Repeat("x", 1<<20)}}}))
	full, err := data.ListDeployments(ctx, 100)
	must(err)
	summaries, err := data.ListDeploymentSummaries(ctx, 100)
	must(err)
	a, err := json.Marshal(full)
	must(err)
	b, err := json.Marshal(summaries)
	must(err)
	if string(a) != string(b) {
		t.Fatal("summary changed public response")
	}
	if full[0].ID != "large-snapshot" || full[0].Snapshot.Values["payload"] == nil {
		t.Fatal("detailed history lost snapshot")
	}
	if summaries[0].Snapshot.Values != nil {
		t.Fatal("summary loaded internal snapshot")
	}
}

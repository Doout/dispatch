package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
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

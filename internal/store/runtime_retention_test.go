package store

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeRetentionDurableFences(t *testing.T) {
	testRuntimeRetentionDurableFences(t, filepath.Join(t.TempDir(), "retention.db"))
}
func TestRuntimeRetentionPostgresFences(t *testing.T) {
	dsn := os.Getenv("DISPATCH_RETENTION_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_RETENTION_POSTGRES_URL to a disposable database")
	}
	testRuntimeRetentionDurableFences(t, dsn)
}
func testRuntimeRetentionDurableFences(t *testing.T, dsn string) {
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
	now := time.Now().UTC()
	p := core.Project{ID: "retention-project", Name: "Retention", CreatedAt: now}
	server := core.Server{ID: "retention-server", Name: "Server", Runtime: core.ServerRuntimeDocker, CreatedAt: now}
	app := core.App{ID: "retention-app", ProjectID: p.ID, ServerID: server.ID, Name: "App", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	must(data.CreateProject(ctx, p))
	must(data.CreateServer(ctx, server))
	must(data.CreateApp(ctx, app))
	d := core.Deployment{ID: "retention-old", AppID: app.ID, State: core.DeploymentFailed, CreatedAt: now.AddDate(0, 0, -30)}
	must(data.CreateDeployment(ctx, d))
	policy := core.RetentionPolicy{ProjectID: p.ID, LogDays: 30, RunDays: 90, KeepRuns: 5, StoppedRevisionDays: 7}
	must(data.SaveRetentionPolicy(ctx, policy))
	item := core.RuntimeRetentionItem{Key: "revision:" + server.ID + ":" + d.ID, Kind: "revision", ServerID: server.ID, AppID: app.ID, DeploymentID: d.ID, Identity: strings.Repeat("a", 64), Protected: []string{}}
	r := core.RuntimeRetentionReview{ID: "cleanup-review", ProjectID: p.ID, Policy: policy, Digest: strings.Repeat("b", 64), State: "planned", CreatedAt: now, ExpiresAt: now.Add(time.Minute), Items: []core.RuntimeRetentionItem{item}, Results: []core.RuntimeRetentionOutcome{}}
	must(data.SaveRuntimeRetentionReview(ctx, r))
	stale := r
	r.Attempt = "attempt-one"
	must(data.ClaimRuntimeRetentionReview(ctx, r, now))
	r.State = "running"
	must(data.BeginRuntimeArtifactRetirement(ctx, r, item))
	// Another accepted receipt cannot take a still-running attempt's fence.
	competing := r
	competing.ID = "competing-review"
	competing.Digest = strings.Repeat("c", 64)
	competing.State = "planned"
	competing.Attempt = "competing-attempt"
	must(data.SaveRuntimeRetentionReview(ctx, competing))
	must(data.ClaimRuntimeRetentionReview(ctx, competing, now))
	competing.State = "running"
	if err = data.BeginRuntimeArtifactRetirement(ctx, competing, item); !errors.Is(err, ErrRuntimeRetentionChanged) {
		t.Fatalf("active retirement claim transferred: %v", err)
	}
	competing.State = "partial"
	must(data.UpdateRuntimeRetentionReview(ctx, competing, true))

	changed := policy
	changed.StoppedRevisionDays = 30
	if err = data.SaveRetentionPolicy(ctx, changed); !errors.Is(err, ErrRuntimeRetentionChanged) {
		t.Fatalf("active cleanup policy changed: %v", err)
	}
	for _, err := range []error{data.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: d.ID, AppID: app.ID, ServerID: server.ID}), data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "future-stage", DeploymentIDs: []string{d.ID}}), data.UpsertPreviewGroupRunComponent(ctx, core.PreviewGroupRunComponent{DeploymentID: d.ID})} {
		if !errors.Is(err, ErrRuntimeRetentionChanged) {
			t.Fatalf("reference acquired retiring inputs: %v", err)
		}
	}
	// A crashed attempt becomes a partial receipt after its lease. Old writers
	// cannot extend or overwrite the new attempt's outcome.
	_, err = data.db.ExecContext(ctx, data.q(`UPDATE runtime_retention_reviews SET lease_until=? WHERE id=?`), stamp(now.Add(-time.Hour)), r.ID)
	must(err)
	recovered, err := data.GetRuntimeRetentionReview(ctx, r.ID)
	must(err)
	if recovered.State != "partial" {
		t.Fatalf("crashed cleanup did not recover: %+v", recovered)
	}
	recovered.Attempt = "attempt-two"
	must(data.ClaimRuntimeRetentionReview(ctx, recovered, now))
	recovered.State = "running"
	if err = data.UpdateRuntimeRetentionReview(ctx, r, false); err == nil {
		t.Fatal("old attempt overwrote new lease")
	}
	if err = data.ValidateRuntimeRetentionMutation(ctx, r.ID, r.Digest, r.Attempt, item, now); !errors.Is(err, ErrRuntimeRetentionChanged) {
		t.Fatalf("old remote attempt renewed: %v", err)
	}
	recovered.Results = []core.RuntimeRetentionOutcome{{Key: item.Key, State: "removed", Message: "removed"}}
	recovered.State = "succeeded"
	must(data.UpdateRuntimeRetentionReview(ctx, recovered, true))
	stale.Attempt = "attempt-stale"
	if err = data.ClaimRuntimeRetentionReview(ctx, stale, now); !errors.Is(err, ErrRuntimeRetentionChanged) {
		t.Fatalf("stale caller forgot completed results: %v", err)
	}
	saved, err := data.GetRuntimeRetentionReview(ctx, r.ID)
	must(err)
	if len(saved.Results) != 1 || saved.Results[0].State != "removed" {
		t.Fatalf("result overwritten %+v", saved)
	}
}

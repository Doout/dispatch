package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestBoundedHistoryRetention(t *testing.T) {
	testBoundedHistoryRetention(t, filepath.Join(t.TempDir(), "history.db"))
}
func TestBoundedHistoryRetentionPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_STORE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	testBoundedHistoryRetention(t, dsn)
}
func testBoundedHistoryRetention(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	must(s.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now}))
	must(s.CreateServer(ctx, core.Server{ID: "s", Name: "s", Runtime: "docker", CreatedAt: now}))
	app := core.App{ID: "a", ProjectID: "p", ServerID: "s", Name: "a", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	must(s.CreateApp(ctx, app))
	for i := 0; i < 30; i++ {
		at := now.Add(-45 * 24 * time.Hour).Add(time.Duration(i) * time.Hour)
		id := fmt.Sprintf("success-%02d", i)
		must(s.CreateDeployment(ctx, core.Deployment{ID: id, AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: at, FinishedAt: &at, Snapshot: core.DeploymentSnapshot{TargetID: "s", Values: map[string]any{"payload": strings.Repeat("x", 32768)}}}))
		must(s.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: id, Message: "old log", CreatedAt: at}))
	}
	policy, err := s.GetRetentionPolicy(ctx, "p")
	must(err)
	if policy != core.DefaultRetentionPolicy("p") {
		t.Fatal("incorrect default policy")
	}
	must(s.SaveRetentionPolicy(ctx, policy))
	must(s.SetDeploymentRetentionPin(ctx, "success-05", true))
	route := core.ApplicationRoute{AppID: "a", ProjectID: "p", ServerID: "s", Hostname: "a.example.test", RequestedDeploymentID: "success-06", DeploymentID: "success-06", State: "active"}
	_, err = s.ReserveApplicationRoute(ctx, route)
	must(err)
	must(s.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: "success-08", AppID: "a", ServerID: "s", Ciphertext: "fixture"}))
	must(s.CreateSecret(ctx, core.Secret{ID: "credential", Name: "credential", Type: core.SecretTypeGitHubToken, EnvironmentVariable: "TOKEN", EncryptedValue: "fixture", CreatedAt: now}))
	must(s.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "p", Name: "source", Repository: "example/repo", CredentialSecretID: "credential", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "resource", ConfigSourceID: "source", Kind: "Application", Name: "resource", State: "valid", Active: true, CreatedAt: now, UpdatedAt: now}))
	old := now.Add(-45 * 24 * time.Hour)
	for _, state := range []string{"succeeded", "running"} {
		must(s.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: state, ResourceID: "resource", State: state, CreatedAt: old}))
		id := "success-00"
		if state == "running" {
			id = "success-07"
		}
		must(s.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: state, RevisionID: state, State: "succeeded", DeploymentIDs: []string{id}, CreatedAt: old}))
		must(s.CreateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: state, ResourceID: "resource", RevisionID: state, JobName: "build", State: "succeeded", Log: "old build log", Outputs: map[string]string{"image": "retained"}, CreatedAt: old, FinishedAt: &old}))
	}
	active := core.Deployment{ID: "active", AppID: "a", State: core.DeploymentBuilding, CreatedAt: now.AddDate(0, 0, -200), Snapshot: core.DeploymentSnapshot{TargetID: "s", Values: map[string]any{"active": true}}}
	must(s.CreateDeployment(ctx, active))
	must(s.CreateProject(ctx, core.Project{ID: "private", Name: "private", CreatedAt: now}))
	other := app
	other.ID = "private-app"
	other.Name = "private-app"
	other.ProjectID = "private"
	must(s.CreateApp(ctx, other))
	must(s.CreateDeployment(ctx, core.Deployment{ID: "private-run", AppID: other.ID, State: core.DeploymentFailed, CreatedAt: old, Snapshot: active.Snapshot}))
	preview, err := s.ApplyRetentionReviewed(ctx, policy, false, now)
	must(err)
	if preview.Snapshots != 6 || preview.Runs != 0 || preview.WorkflowLogs != 1 || preview.SnapshotBytes < 6*32768 {
		t.Fatalf("unexpected preview %+v", preview)
	}
	before, err := s.GetDeployment(ctx, "success-00")
	must(err)
	if before.InputsExpiredAt != nil || before.Snapshot.Values == nil {
		t.Fatal("preview removed inputs")
	}
	result, err := s.ApplyRetentionReviewed(ctx, policy, true, now)
	must(err)
	if result.Snapshots != preview.Snapshots || result.Logs != preview.Logs {
		t.Fatal("applied cleanup differs from preview")
	}
	expired, err := s.GetDeployment(ctx, "success-00")
	must(err)
	if expired.InputsExpiredAt == nil || expired.Snapshot.Values != nil || expired.CommitSHA != before.CommitSHA {
		t.Fatal("snapshot did not expire independently of summary")
	}
	for _, id := range []string{"success-05", "success-06", "success-07", "success-08", "success-29", "active", "private-run"} {
		d, err := s.GetDeployment(ctx, id)
		must(err)
		if d.InputsExpiredAt != nil || d.Snapshot.Values == nil {
			t.Fatalf("protected inputs expired: %s", id)
		}
	}
	if err = s.SetDeploymentRetentionPin(ctx, expired.ID, true); !errors.Is(err, ErrDeploymentInputsExpired) {
		t.Fatal("expired inputs could be pinned", err)
	}
	if err = s.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: expired.ID, AppID: "a", ServerID: "s"}); !errors.Is(err, ErrDeploymentInputsExpired) {
		t.Fatal("expired inputs acquired a new reference", err)
	}
	if err = s.CreateRollbackDeployment(ctx, core.Deployment{ID: "rollback", AppID: "a", State: core.DeploymentQueued, CreatedAt: now}, before, core.ReleaseAction{}); !errors.Is(err, ErrDeploymentInputsExpired) {
		t.Fatal("stale rollback restored expired inputs", err)
	}
	completed, err := s.ListWorkflowJobResults(ctx, "succeeded")
	must(err)
	if len(completed) != 1 || completed[0].Log != "" || completed[0].Outputs["image"] != "retained" {
		t.Fatal("job log retention removed reusable results")
	}
	running, err := s.ListWorkflowJobResults(ctx, "running")
	must(err)
	if len(running) != 1 || running[0].Log == "" {
		t.Fatal("active workflow lost its logs")
	}
	again, err := s.ApplyRetentionReviewed(ctx, policy, true, now)
	must(err)
	if again.Snapshots+again.Logs+again.WorkflowLogs+again.Runs != 0 {
		t.Fatal("cleanup is not idempotent")
	}
	policy.RunDays = 31
	must(s.SaveRetentionPolicy(ctx, policy))
	result, err = s.ApplyRetentionReviewed(ctx, policy, true, now)
	must(err)
	if result.Runs != 6 {
		t.Fatalf("older successful summaries remain permanently protected: %+v", result)
	}
	if _, err = s.GetDeployment(ctx, "success-00"); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired successful summary retained", err)
	}
	policy.PauseAutomatic = true
	must(s.SaveRetentionPolicy(ctx, policy))
	policies, err := s.ListAutomaticRetentionPolicies(ctx)
	must(err)
	for _, p := range policies {
		if p.ProjectID == "p" {
			t.Fatal("paused project selected for automatic cleanup")
		}
	}
}

func TestHistoryRetentionPostgresPinRace(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_STORE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	must(s.SeedDemo(ctx))
	apps, err := s.ListApps(ctx)
	must(err)
	app := apps[0]
	now := time.Now().UTC()
	policy := core.DefaultRetentionPolicy(app.ProjectID)
	must(s.SaveRetentionPolicy(ctx, policy))
	for i := 0; i < 23; i++ {
		at := now.AddDate(0, 0, -40).Add(time.Duration(i) * time.Hour)
		must(s.CreateDeployment(ctx, core.Deployment{ID: fmt.Sprintf("race-%02d", i), AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: at, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: map[string]any{"replicas": i}}}))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	must(err)
	defer tx.Rollback()
	var id string
	must(tx.QueryRowContext(ctx, `SELECT id FROM deployments WHERE id='race-00' FOR UPDATE`).Scan(&id))
	result := make(chan error, 1)
	go func() { _, err := s.ApplyRetentionReviewed(ctx, policy, true, now); result <- err }()
	time.Sleep(50 * time.Millisecond)
	_, err = tx.ExecContext(ctx, `UPDATE deployments SET retention_pinned=TRUE WHERE id='race-00'`)
	must(err)
	must(tx.Commit())
	select {
	case err := <-result:
		must(err)
	case <-ctx.Done():
		t.Fatal("cleanup did not release its locks")
	}
	d, err := s.GetDeployment(ctx, "race-00")
	must(err)
	if d.InputsExpiredAt != nil || !d.RetentionPinned {
		t.Fatal("concurrent pin did not protect inputs")
	}
}

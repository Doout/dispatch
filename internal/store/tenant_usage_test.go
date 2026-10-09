package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestTenantUsageSQLite(t *testing.T) { testTenantUsage(t, filepath.Join(t.TempDir(), "usage.db")) }
func TestTenantUsagePostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("PostgreSQL not configured")
	}
	testTenantUsage(t, dsn)
}
func testTenantUsage(t *testing.T, dsn string) {
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
	must(s.SeedDemo(ctx))
	projects, err := s.ListProjects(ctx)
	must(err)
	apps, err := s.ListApps(ctx)
	must(err)
	now := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	initial, err := s.TenantUsage(ctx, start, end)
	must(err)
	if initial.Builds != 0 || initial.Deployments != 0 || initial.BuildSeconds != 0 || initial.Projects != int64(len(projects)) {
		t.Fatalf("wrong empty period: %+v", initial)
	}
	must(s.CreateSecret(ctx, core.Secret{ID: "usage-credential", Name: "usage-credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture-value", CreatedAt: now}))
	source := core.ConfigSource{ID: "usage-source", ProjectID: projects[0].ID, CredentialSecretID: "usage-credential", Name: "private-repo-name", Repository: "private/repo", CreatedAt: now, UpdatedAt: now}
	must(s.CreateConfigSource(ctx, source))
	resource := core.WorkflowResource{ID: "usage-resource", ConfigSourceID: source.ID, Name: "private-app", Path: "usage.yaml", Kind: "Application", CreatedAt: now, UpdatedAt: now}
	must(s.CreateWorkflowResource(ctx, resource))
	revision := core.WorkflowRevision{ID: "usage-revision", ResourceID: resource.ID, State: "running", CreatedAt: now}
	must(s.CreateWorkflowRevision(ctx, revision))
	for i, fixture := range []struct {
		state, reused string
		finish        time.Time
		duration      time.Duration
	}{{"succeeded", "", now, 30 * time.Second}, {"failed", "", now, 45 * time.Second}, {"succeeded", "original-result", now, 200 * time.Second}, {"running", "", now, 80 * time.Second}, {"succeeded", "", end, 90 * time.Second}, {"cancelled", "", start, 5 * time.Second}, {"succeeded", "", now, -10 * time.Second}, {"succeeded", "", start.Add(500 * time.Millisecond), 3 * time.Second}, {"succeeded", "", end.Add(-500 * time.Millisecond), 3 * time.Second}, {"succeeded", "", end.Add(500 * time.Millisecond), 90 * time.Second}, {"succeeded", "", start.Add(-500 * time.Millisecond), 90 * time.Second}} {
		begin := fixture.finish.Add(-fixture.duration)
		finish := fixture.finish
		job := core.WorkflowJobResult{ID: fmt.Sprintf("usage-job-%d", i), ResourceID: resource.ID, RevisionID: revision.ID, JobName: "sensitive-name", State: fixture.state, ReusedFromID: fixture.reused, Log: "private output", CreatedAt: begin, StartedAt: &begin, FinishedAt: &finish}
		must(s.CreateWorkflowJobResult(ctx, job))
	}
	for i, finish := range []time.Time{now, end, start.Add(-time.Second)} {
		finished := finish
		must(s.CreateDeployment(ctx, core.Deployment{ID: fmt.Sprintf("usage-deploy-%d", i), AppID: apps[0].ID, State: core.DeploymentSucceeded, CreatedAt: finish, FinishedAt: &finished}))
	}
	usage, err := s.TenantUsage(ctx, start, end)
	must(err)
	if usage.Builds != 6 || usage.BuildSeconds != 86 || usage.Deployments != 1 {
		t.Fatalf("wrong completed usage: %+v", usage)
	}
	for _, metric := range usage.Measured {
		if metric == "storageBytes" || metric == "transferBytes" || metric == "runtimeSeconds" {
			t.Fatal("unmeasured resource claimed", metric)
		}
	}
	if _, err = s.TenantUsage(ctx, end, start); err == nil {
		t.Fatal("reversed usage period accepted")
	}
}

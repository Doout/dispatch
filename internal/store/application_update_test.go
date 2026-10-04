package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func applicationConfigurationFixture(t *testing.T, dsn string) (*SQLStore, core.App, core.Deployment) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	id := fmt.Sprintf("configuration-%d", now.UnixNano())
	must(s.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}))
	must(s.CreateServer(ctx, core.Server{ID: id + "-target", Name: "Target", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: now}))
	app := core.App{ID: id + "-app", ProjectID: id, ServerID: id + "-target", Name: "Service", SourceRepo: "https://example.test/service.git", Branch: "main",
		BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile", ComposePath: "compose.yml", ContainerPort: 8080,
		PreDeployHook: "echo before", HookEnvironment: map[string]string{"PRIVATE": "encrypted-hook-value"}, State: "live", CreatedAt: now}
	must(s.CreateApp(ctx, app))
	d := core.Deployment{ID: id + "-deployment", AppID: app.ID, State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CommitSHA: "original-commit",
		Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: map[string]any{"revision": "original"}}, CreatedAt: now, FinishedAt: &now}
	must(s.CreateDeployment(ctx, d))
	return s, app, d
}

func TestApplicationConfigurationSQLite(t *testing.T) {
	testApplicationConfiguration(t, filepath.Join(t.TempDir(), "config.db"))
}

func TestApplicationConfigurationPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL to a disposable database")
	}
	testApplicationConfiguration(t, dsn)
}

func testApplicationConfiguration(t *testing.T, dsn string) {
	s, app, deployment := applicationConfigurationFixture(t, dsn)
	ctx := context.Background()
	updated := app
	updated.Branch, updated.ContextPath, updated.Domain, updated.ContainerPort = "release", "backend", "api.example.test", 9000
	if err := s.UpdateAppConfiguration(ctx, updated, app.SpecDigest()); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetApp(ctx, app.ID)
	if err != nil || saved.SpecDigest() != updated.SpecDigest() || saved.Name != app.Name || saved.State != app.State || saved.ServerID != app.ServerID || saved.ProjectID != app.ProjectID || !saved.CreatedAt.Equal(app.CreatedAt) || saved.HookEnvironment["PRIVATE"] != app.HookEnvironment["PRIVATE"] {
		t.Fatal("update changed identity, hooks or saved inputs", saved, err)
	}
	history, err := s.GetDeployment(ctx, deployment.ID)
	if err != nil || history.SpecDigest != deployment.SpecDigest || history.CommitSHA != deployment.CommitSHA || history.Snapshot.Values["revision"] != "original" {
		t.Fatal("update changed the retained deployment", history, err)
	}
	if err := s.UpdateAppConfiguration(ctx, app, app.SpecDigest()); !errors.Is(err, ErrApplicationConfigurationChanged) {
		t.Fatal("stale edit overwrote saved inputs", err)
	}
	updated.Name = "Renamed"
	if err := s.UpdateAppConfiguration(ctx, updated, saved.SpecDigest()); !errors.Is(err, ErrApplicationConfigurationChanged) {
		t.Fatal("configuration update changed application identity", err)
	}
	updated = saved
	updated.PostDeployHook = "new saved hook"
	if err := s.UpdateApp(ctx, updated); err != nil {
		t.Fatal(err)
	}
	expected := saved.SpecDigest()
	saved.Branch = "stale-after-hook-edit"
	if err := s.UpdateAppConfiguration(ctx, saved, expected); !errors.Is(err, ErrApplicationConfigurationChanged) {
		t.Fatal("concurrent hook change was omitted from the configuration check", err)
	}
	updated, _ = s.GetApp(ctx, app.ID)
	queued := deployment
	queued.ID += "-queued"
	queued.State = core.DeploymentQueued
	queued.FinishedAt = nil
	if err := s.CreateDeployment(ctx, queued); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAppConfiguration(ctx, updated, updated.SpecDigest()); !errors.Is(err, ErrAppActive) {
		t.Fatal("active deployment admitted configuration edit", err)
	}
}

func TestApplicationConfigurationConcurrentCompareAndSwap(t *testing.T) {
	s, app, _ := applicationConfigurationFixture(t, filepath.Join(t.TempDir(), "config.db"))
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, branch := range []string{"first-edit", "second-edit"} {
		wg.Add(1)
		go func(branch string) {
			defer wg.Done()
			<-start
			candidate := app
			candidate.Branch = branch
			results <- s.UpdateAppConfiguration(context.Background(), candidate, app.SpecDigest())
		}(branch)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrApplicationConfigurationChanged) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal("concurrent editors lost an update", success, conflicts)
	}
}

func TestApplicationConfigurationRejectsGeneratedAndSourceOwnedApps(t *testing.T) {
	for _, owner := range []string{"generated", "workflow-resource", "workflow-revision"} {
		t.Run(owner, func(t *testing.T) {
			s, app, _ := applicationConfigurationFixture(t, filepath.Join(t.TempDir(), "config.db"))
			if owner == "generated" {
				app.Generated = true
			}
			if owner == "workflow-resource" {
				now := time.Now().UTC()
				if err := s.CreateSecret(context.Background(), core.Secret{ID: "config-credential", Name: "Config credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "encrypted-fixture", CreatedAt: now}); err != nil {
					t.Fatal(err)
				}
				if err := s.CreateConfigSource(context.Background(), core.ConfigSource{ID: "config-source", ProjectID: app.ProjectID, CredentialSecretID: "config-credential", Name: "Config source", CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
				if err := s.CreateWorkflowResource(context.Background(), core.WorkflowResource{ID: "source-resource", ConfigSourceID: "config-source", Kind: "Application", Name: "Source resource", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
					t.Fatal(err)
				}
				app.HelmProvenance.WorkflowResourceID = "source-resource"
			}
			if owner == "workflow-revision" {
				app.HelmProvenance.WorkflowRevisionID = "source-revision"
			}
			if err := s.UpdateApp(context.Background(), app); err != nil {
				t.Fatal(err)
			}
			if err := s.UpdateAppConfiguration(context.Background(), app, app.SpecDigest()); !errors.Is(err, ErrApplicationConfigurationManaged) {
				t.Fatal("source-owned application became manually editable", err)
			}
		})
	}
}

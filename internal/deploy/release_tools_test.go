package deploy

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestRetainedServiceSecretUsesStoredReference(t *testing.T) {
	binding := core.ServiceBinding{Helm: &core.ServiceHelmBinding{SecretNameValues: []string{"database.existingSecret", "worker.existingSecret"}}}
	values := map[string]any{"database": map[string]any{"existingSecret": "dispatch-svc-original-0"}, "worker": map[string]any{"existingSecret": "dispatch-svc-original-0"}}
	if got := retainedServiceSecretName(values, binding); got != "dispatch-svc-original-0" {
		t.Fatalf("lost original reference %q", got)
	}
	values["worker"] = map[string]any{"existingSecret": "dispatch-svc-different-0"}
	if retainedServiceSecretName(values, binding) != "" {
		t.Fatal("accepted mismatched credential references")
	}
	values["database"] = map[string]any{"existingSecret": "user-managed"}
	values["worker"] = values["database"]
	if retainedServiceSecretName(values, binding) != "" {
		t.Fatal("accepted unrelated Secret")
	}
}

func TestRuntimePreviewPinsSourceAndValidatesComposeSelections(t *testing.T) {
	repo := t.TempDir()
	serviceFixtureRepo(t, repo, map[string]string{"compose.yaml": "services:\n  api:\n    image: example.test/application:v1\n"})
	app := core.App{SourceRepo: "file://" + repo, Branch: "main", BuildType: core.BuildTypeCompose, ContextPath: ".", ComposePath: "compose.yaml"}
	app.ServiceRuntime = []core.ServiceRuntimeBinding{{Binding: core.ServiceBinding{Compose: map[string]map[string]string{"api": {"DATABASE_URL": "url"}}}, Values: map[string]string{"url": "private-value"}}}
	resolved, err := previewRuntimeSource(context.Background(), app, "HEAD")
	if err != nil || len(resolved) != 40 {
		t.Fatalf("preview did not resolve a fixed Git commit: %q %v", resolved, err)
	}
	app.ServiceRuntime[0].Binding.Compose = map[string]map[string]string{"missing": {"DATABASE_URL": "url"}}
	if _, err = previewRuntimeSource(context.Background(), app, resolved); err == nil {
		t.Fatal("preview accepted an unknown selected Compose service")
	}
}

func TestRollbackUnavailableDoesNotCreateDeployment(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "rollback.db"))
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
	app := apps[0]
	app.ID = "rollback-test-app"
	app.Name = "rollback-test-app"
	app.Template = false
	must(data.CreateApp(ctx, app))
	now := time.Now().UTC()
	source := core.Deployment{ID: "rollback-source", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now, FinishedAt: &now}
	must(data.CreateDeployment(ctx, source))
	s := NewService(data, SimulationExecutor{})
	preview, err := s.PreviewRollback(ctx, source.ID)
	must(err)
	if preview.Available || preview.Message == "" {
		t.Fatal("historical artifacts were assumed available")
	}
	if _, err = s.StartRollback(ctx, source.ID, "stale-current", "operator", nil); err == nil {
		t.Fatal("stale rollback confirmation accepted")
	}
	if _, err = s.StartRollback(ctx, source.ID, source.ID, "operator", nil); err == nil {
		t.Fatal("rollback without retained artifacts accepted")
	}
	history, err := data.ListApplicationHistory(ctx, app.ID, "", 100)
	must(err)
	for _, d := range history {
		if d.State == core.DeploymentQueued {
			t.Fatal("failed validation queued a deployment")
		}
	}
}

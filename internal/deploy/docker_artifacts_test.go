package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

func runtimeFixture(t *testing.T, build core.BuildType) (*store.SQLStore, DockerExecutor, core.App, core.Server, core.Deployment) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "master.key")
	if err = os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "runtime-project", Name: "Runtime project", CreatedAt: now}
	server := core.Server{ID: "runtime-server", Name: "Local target", Address: "local", Runtime: "docker", State: "ready", CreatedAt: now}
	app := core.App{ID: "runtime-app", Name: "Runtime application", ProjectID: project.ID, ServerID: server.ID, BuildType: build, CreatedAt: now}
	for _, err := range []error{data.CreateProject(ctx, project), data.CreateServer(ctx, server), data.CreateApp(ctx, app)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	source := core.Deployment{ID: "runtime-source", AppID: app.ID, CommitSHA: "original-revision", State: core.DeploymentSucceeded, CreatedAt: now, FinishedAt: &now, Snapshot: core.DeploymentSnapshot{TargetID: server.ID, Runtime: string(build)}}
	if err = data.CreateDeployment(ctx, source); err != nil {
		t.Fatal(err)
	}
	return data, DockerExecutor{Artifacts: data, Vault: vault, ArtifactDirectory: t.TempDir()}, app, server, source
}

func TestRuntimeArtifactEncryptionAndValidation(t *testing.T) {
	data, e, app, server, source := runtimeFixture(t, core.BuildTypeDockerfile)
	image := "sha256:" + strings.Repeat("a", 64)
	inputs := dockerArtifact{Version: 1, BuildType: app.BuildType, Images: map[string]string{"application": image}, Bindings: []core.ServiceRuntimeBinding{{Values: map[string]string{"password": "private-runtime-value"}}}}
	e.run = func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		if args[0] == "image" {
			io.WriteString(out, image+"\n")
		}
		return nil
	}
	if err := e.saveArtifact(context.Background(), source, app, server, inputs); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetRuntimeArtifact(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.Ciphertext, "private-runtime-value") {
		t.Fatal("runtime credentials stored as plaintext")
	}
	loaded, err := e.loadArtifact(context.Background(), source, app, server)
	if err != nil || loaded.Bindings[0].Values["password"] != "private-runtime-value" {
		t.Fatalf("retained inputs lost: %v", err)
	}
	if err = e.saveArtifact(context.Background(), source, app, server, inputs); err == nil {
		t.Fatal("immutable artifact overwritten")
	}
	e.run = func(context.Context, io.Reader, io.Writer, string, ...string) error {
		return errors.New("image missing")
	}
	if _, err = e.loadArtifact(context.Background(), source, app, server); err == nil {
		t.Fatal("missing image accepted")
	}
	app.ServerID = "other-target"
	if _, err = e.loadArtifact(context.Background(), source, app, server); err == nil {
		t.Fatal("changed target accepted")
	}
}

func TestRetainedComposeFilesAndInterpolation(t *testing.T) {
	_, e, app, _, d := runtimeFixture(t, core.BuildTypeCompose)
	workspace := t.TempDir()
	if err := os.Mkdir(filepath.Join(workspace, "config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(workspace, "config", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "config", "settings"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	image := "sha256:" + strings.Repeat("a", 64)
	inputs := dockerArtifact{Version: 1, BuildType: app.BuildType, Images: map[string]string{"api": image}, Files: map[string]runtimeFile{}, Compose: map[string]any{"services": map[string]any{"api": map[string]any{"image": image, "labels": map[string]any{"dispatch.app": app.ID}, "environment": map[string]any{"VALUE": "literal$$PASSWORD"}, "volumes": []any{map[string]any{"type": "bind", "source": filepath.Join(workspace, "config"), "target": "/config", "read_only": true}}}}}}
	if err := retainComposeRuntimeFiles(&inputs, workspace); err != nil {
		t.Fatal(err)
	}
	path, err := e.materializeCompose(d, app, inputs)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "literal$$PASSWORD") {
		t.Fatal("resolved environment would be interpolated again")
	}
	if _, err = os.Stat(filepath.Join(filepath.Dir(path), "files", "config", "empty")); err != nil {
		t.Fatal("empty runtime directory lost")
	}
	info, err := os.Stat(filepath.Join(filepath.Dir(path), "files", "config"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("mounted directory permissions changed")
	}
	original := inputs.Compose["services"].(map[string]any)["api"].(map[string]any)["volumes"].([]any)[0].(map[string]any)["source"]
	if original != retainedFilePrefix+"config" {
		t.Fatal("materialization mutated immutable inputs")
	}
	inputs.Files["../escape"] = runtimeFile{Data: []byte("unsafe")}
	if _, err = e.materializeCompose(d, app, inputs); err == nil {
		t.Fatal("escaping runtime path accepted")
	}
}

type rollbackFixtureExecutor struct {
	preview func() RollbackPreview
	apply   func(context.Context, Progress) error
}

func (e rollbackFixtureExecutor) Deploy(context.Context, core.Deployment, core.App, core.Server, Progress) error {
	return nil
}
func (e rollbackFixtureExecutor) PreviewRuntimeRollback(context.Context, core.Deployment, core.App, core.Server) (RollbackPreview, error) {
	return e.preview(), nil
}
func (e rollbackFixtureExecutor) RollbackRuntime(ctx context.Context, _ core.Deployment, _ core.Deployment, _ core.App, _ core.Server, p Progress) error {
	return e.apply(ctx, p)
}

func waitRuntimeDeployment(t *testing.T, data *store.SQLStore, id string) core.Deployment {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		d, err := data.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.State == core.DeploymentSucceeded || d.State == core.DeploymentFailed || d.State == core.DeploymentCancelled {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("runtime deployment did not finish")
	return core.Deployment{}
}

func TestRuntimeRollbackStaleReviewFailureCancellationAndRetry(t *testing.T) {
	data, _, app, _, source := runtimeFixture(t, core.BuildTypeDockerfile)
	version := "runtime-one"
	apply := func(_ context.Context, p Progress) error {
		if err := p(core.DeploymentStarting, "Starting retained runtime"); err != nil {
			return err
		}
		return errors.New("Runtime apply stopped after removing the old container; inspect before retrying")
	}
	e := rollbackFixtureExecutor{preview: func() RollbackPreview { return RollbackPreview{Available: true, RuntimeDigest: version} }, apply: func(ctx context.Context, p Progress) error { return apply(ctx, p) }}
	s := NewService(data, e)
	review, err := s.PreviewRollback(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	version = "external-release"
	if _, err = s.StartRollback(context.Background(), source.ID, source.ID, review.ReviewDigest, "operator", nil); err == nil {
		t.Fatal("external runtime change accepted")
	}
	review, err = s.PreviewRollback(context.Background(), source.ID)
	if err != nil {
		t.Fatal(err)
	}
	app.Domain = "changed.example.test"
	if err = data.UpdateApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if _, err = s.StartRollback(context.Background(), source.ID, source.ID, review.ReviewDigest, "operator", nil); err == nil {
		t.Fatal("changed routing accepted")
	}
	start := func() core.Deployment {
		t.Helper()
		r, err := s.PreviewRollback(context.Background(), source.ID)
		if err != nil {
			t.Fatal(err)
		}
		d, err := s.StartRollback(context.Background(), source.ID, r.CurrentDeploymentID, r.ReviewDigest, "operator", nil)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	failed := waitRuntimeDeployment(t, data, start().ID)
	if failed.State != core.DeploymentFailed || !strings.Contains(failed.Message, "inspect before retrying") {
		t.Fatalf("partial failure hidden: %+v", failed)
	}
	entered := make(chan struct{})
	apply = func(ctx context.Context, p Progress) error {
		if err := p(core.DeploymentStarting, "Restoring"); err != nil {
			return err
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	cancelled := start()
	<-entered
	if err = s.Cancel(context.Background(), cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if got := waitRuntimeDeployment(t, data, cancelled.ID); got.State != core.DeploymentCancelled {
		t.Fatalf("cancellation hidden: %+v", got)
	}
	apply = func(_ context.Context, p Progress) error {
		return p(core.DeploymentChecking, "Healthy retained revision")
	}
	succeeded := waitRuntimeDeployment(t, data, start().ID)
	if succeeded.State != core.DeploymentSucceeded || succeeded.ID == source.ID || succeeded.CommitSHA != source.CommitSHA {
		t.Fatalf("retry lost immutable history: %+v", succeeded)
	}
	original, err := data.GetDeployment(context.Background(), source.ID)
	if err != nil || original.State != core.DeploymentSucceeded {
		t.Fatal("source history rewritten")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		actions, err := data.ListReleaseActions(context.Background(), app.ProjectID, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		outcomes := map[string]bool{}
		for _, action := range actions {
			if action.Actor == "operator" && action.SourceDeploymentID == source.ID {
				outcomes[action.Action] = true
			}
		}
		if outcomes["deployment.rollback.failed"] && outcomes["deployment.rollback.cancelled"] && outcomes["deployment.rollback.succeeded"] {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rollback outcome audit did not retain the actor and source revision")
}

func TestRetainedComposeShapeValidation(t *testing.T) {
	_, e, app, _, d := runtimeFixture(t, core.BuildTypeCompose)
	for _, config := range []map[string]any{nil, {"services": map[string]any{"api": nil}}, {"services": map[string]any{"api": map[string]any{"image": "mutable:latest"}}}} {
		inputs := dockerArtifact{Compose: config, Images: map[string]string{"api": "sha256:" + strings.Repeat("a", 64)}}
		if _, err := e.materializeCompose(d, app, inputs); err == nil {
			raw, _ := json.Marshal(config)
			t.Fatalf("invalid inputs accepted: %s", raw)
		}
	}
}

func TestRuntimeOwnershipConflictStopsBeforeMutation(t *testing.T) {
	_, e, app, server, _ := runtimeFixture(t, core.BuildTypeDockerfile)
	writes := 0
	e.run = func(_ context.Context, _ io.Reader, out io.Writer, _ string, args ...string) error {
		switch args[0] {
		case "ps":
			io.WriteString(out, "container-id\n")
		case "inspect":
			io.WriteString(out, "another-app\n")
		case "rm", "run", "create":
			writes++
		}
		return nil
	}
	if err := e.cleanupOwnedDocker(context.Background(), app, server, func(core.DeploymentState, string) error { return nil }); err == nil {
		t.Fatal("unowned container cleanup accepted")
	}
	if err := e.removeOwnedContainer(context.Background(), app); err == nil {
		t.Fatal("unowned container replacement accepted")
	}
	if writes != 0 {
		t.Fatal("ownership failure mutated runtime")
	}
}

func TestComposeProfilesAreCapturedAtDeployTime(t *testing.T) {
	t.Setenv("COMPOSE_PROFILES", "qa")
	config := map[string]any{"services": map[string]any{"always": map[string]any{}, "qa": map[string]any{"profiles": []any{"qa"}}, "expensive": map[string]any{"profiles": []any{"expensive"}}}}
	filterComposeProfiles(config)
	services := config["services"].(map[string]any)
	if len(services) != 2 || services["expensive"] != nil || services["qa"].(map[string]any)["profiles"] != nil {
		t.Fatal("retained services depend on future profile selection")
	}
}

func TestComposeRollbackReviewUsesResolvedPublishedPorts(t *testing.T) {
	config := map[string]any{"services": map[string]any{"api": map[string]any{"ports": []any{map[string]any{"host_ip": "127.0.0.1", "published": "8080", "target": float64(80), "protocol": "tcp"}, map[string]any{"target": float64(81)}}}}}
	got := composePublishedPorts(config)
	if len(got) != 2 || got[0] != "api: [127.0.0.1]:8080 -> 80/tcp" || got[1] != "api: automatic -> 81/tcp" {
		t.Fatalf("incorrect retained ports: %v", got)
	}
}

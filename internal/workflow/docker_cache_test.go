package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestDockerCredentialsAreIsolatedWithoutChangingDaemon(t *testing.T) {
	original := t.TempDir()
	t.Setenv("DOCKER_CONFIG", original)
	want := `{"auths":{"registry.example.com":{"auth":"test-only"}}}`
	if err := os.WriteFile(filepath.Join(original, "config.json"), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := isolatedDockerConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(first)
	second, err := isolatedDockerConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(second)
	if first == second || first == original {
		t.Fatal("jobs share Docker credential files")
	}
	assertFileContents(t, filepath.Join(first, "config.json"), want)
	if err := os.WriteFile(filepath.Join(first, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, filepath.Join(second, "config.json"), want)
	assertFileContents(t, filepath.Join(original, "config.json"), want)
	info, err := os.Stat(filepath.Join(second, "config.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file permissions: %v %v", info, err)
	}
}

func TestDockerBuilderCacheAffinityIsStableAndUsesFreeCapacity(t *testing.T) {
	runtime, _ := schedulerFixture(t)
	ctx := context.Background()
	for _, id := range []string{"one", "two"} {
		if err := runtime.service.Store.CreateServer(ctx, core.Server{ID: id, Name: id, Runtime: core.ServerRuntimeBuilder,
			Address: "ssh://build@example.com", State: "ready", Builder: &core.BuilderServerConfig{MaxConcurrent: 1}, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	first, release, err := runtime.service.acquireDockerBuilder(ctx, "source-config:service")
	if err != nil {
		t.Fatal(err)
	}
	release()
	again, releaseAgain, err := runtime.service.acquireDockerBuilder(ctx, "source-config:service")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAgain()
	if first.ID != again.ID {
		t.Fatal("same build inputs moved away from the preferred cache")
	}
	fallback, releaseFallback, err := runtime.service.acquireDockerBuilder(ctx, "source-config:service")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseFallback()
	if fallback.ID == first.ID {
		t.Fatal("full preferred builder blocked an available builder")
	}
}

func TestBuilderCacheKeySurvivesNewCommitsAndPreviewIDs(t *testing.T) {
	first := &jobRuntime{source: core.ConfigSource{ID: "config"}, revision: core.WorkflowRevision{ID: "preview-one", Sources: map[string]core.WorkflowSourceRevision{
		"service": {Repository: "example/service", CommitSHA: "old"},
	}}}
	second := &jobRuntime{source: first.source, revision: core.WorkflowRevision{ID: "preview-two", Sources: map[string]core.WorkflowSourceRevision{
		"service": {Repository: "example/service", CommitSHA: "new"},
	}}}
	job := JobSpec{RunFrom: "service"}
	if first.builderCacheKey(job) != second.builderCacheKey(job) {
		t.Fatal("new commit or preview lost builder cache affinity")
	}
	second.source.ID = "different-configuration"
	if first.builderCacheKey(job) == second.builderCacheKey(job) {
		t.Fatal("cache affinity crossed configuration sources")
	}
}

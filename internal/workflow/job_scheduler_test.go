package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/secretvalue"
)

func schedulerFixture(t *testing.T) (*jobRuntime, core.WorkflowResource) {
	t.Helper()
	data, source, resource := workflowRunnerFixture(t, "scheduler")
	revision := core.WorkflowRevision{ID: "scheduler-run", ResourceID: resource.ID, State: "running", Sources: map[string]core.WorkflowSourceRevision{}, CreatedAt: time.Now().UTC()}
	if err := data.CreateWorkflowRevision(context.Background(), revision); err != nil {
		t.Fatal(err)
	}
	return &jobRuntime{service: &Service{Store: data}, source: source, revision: revision, root: t.TempDir(), paths: map[string]string{}}, resource
}

func TestApplicationJobsOverlapAndIsolateWorkingDirectories(t *testing.T) {
	runtime, resource := schedulerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	barrier := t.TempDir()
	jobs := map[string]JobSpec{}
	for _, name := range []string{"service", "ui"} {
		other := "service"
		if name == "service" {
			other = "ui"
		}
		jobs[name] = JobSpec{Reuse: "never", Outputs: []string{"workspace"}, Run: fmt.Sprintf(`
printf %s > mutable
touch %s
while [ ! -f %s ]; do sleep 0.01; done
test "$(cat mutable)" = %s
printf 'workspace=%%s\n' "$PWD" > "$DISPATCH_OUTPUT_FILE"
`, shellEscape(name), shellEscape(filepath.Join(barrier, name)), shellEscape(filepath.Join(barrier, other)), shellEscape(name))}
	}
	outputs, err := runtime.executeJobs(ctx, resource, jobs, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if outputs["service"]["workspace"] == outputs["ui"]["workspace"] {
		t.Fatal("concurrent jobs shared a mutable working directory")
	}
	results, err := runtime.service.Store.ListWorkflowJobResults(ctx, runtime.revision.ID)
	if err != nil || len(results) != 2 {
		t.Fatalf("missing job results: %v %v", results, err)
	}
	if !results[0].StartedAt.Before(*results[1].FinishedAt) || !results[1].StartedAt.Before(*results[0].FinishedAt) {
		t.Fatal("jobs did not overlap")
	}
}

func TestParallelJobsIsolateSharedRepositoryAndReuseResults(t *testing.T) {
	runtime, resource := schedulerFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	repo := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	if err := os.WriteFile(filepath.Join(repo, "tracked"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--initial-branch=main", repo}, {"-C", repo, "add", "."}, {"-C", repo, "commit", "-m", "Test repository"}} {
		if err := runGit(ctx, env, args...); err != nil {
			t.Fatal(err)
		}
	}
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt("secret:checkout", []byte("test-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := runtime.service.Store.CreateSecret(ctx, core.Secret{ID: "checkout", Name: "checkout", Type: core.SecretTypeGitHubToken,
		EncryptedValue: encrypted, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runtime.source.GitHubAppID, runtime.source.CredentialSecretID = "", "checkout"
	runtime.service.Secrets = secretvalue.New(runtime.service.Store, vault)
	runtime.service.Repositories = newRepositoryCache(t.TempDir())
	runtime.revision.Sources["config"] = core.WorkflowSourceRevision{Alias: "config", Repository: "file://" + repo, Branch: "main", CommitSHA: gitOutput(t, repo, "rev-parse", "HEAD")}
	barrier := t.TempDir()
	jobs := map[string]JobSpec{}
	for _, name := range []string{"service", "ui"} {
		other := "service"
		if name == "service" {
			other = "ui"
		}
		jobs[name] = JobSpec{RunFrom: "config", Reuse: "onInputMatch", Outputs: []string{"component"}, Run: fmt.Sprintf(`
printf %s > tracked
touch %s
while [ ! -f %s ]; do sleep 0.01; done
test "$(cat tracked)" = %s
printf 'component=%s\n' > "$DISPATCH_OUTPUT_FILE"
`, shellEscape(name), shellEscape(filepath.Join(barrier, name)), shellEscape(filepath.Join(barrier, other)), shellEscape(name), name)}
	}
	if _, err := runtime.executeJobs(ctx, resource, jobs, nil, false); err != nil {
		t.Fatal(err)
	}
	assertFileContents(t, filepath.Join(repo, "tracked"), "original")
	mirrors, err := filepath.Glob(filepath.Join(runtime.service.Repositories.root, "mirrors", "*.git"))
	if err != nil || len(mirrors) != 1 {
		t.Fatalf("jobs did not share one repository mirror: %v %v", mirrors, err)
	}
	for _, name := range []string{"service", "ui"} {
		if _, err := os.Stat(filepath.Join(runtime.root, "jobs", name, "sources", "config")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("job worktree was not released", err)
		}
	}
	runtime.revision.ID = "repeat"
	if err := runtime.service.Store.CreateWorkflowRevision(ctx, runtime.revision); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.executeJobs(ctx, resource, jobs, nil, false); err != nil {
		t.Fatal(err)
	}
	results, _ := runtime.service.Store.ListWorkflowJobResults(ctx, "repeat")
	if len(results) != 2 || results[0].ReusedFromID == "" || results[1].ReusedFromID == "" {
		t.Fatal("parallel jobs lost successful result reuse", results)
	}
}

func TestJobDependenciesAndConcurrencyLimit(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			runtime, resource := schedulerFixture(t)
			runtime.maxParallelJobs = limit
			marker := filepath.Join(t.TempDir(), "ready")
			jobs := map[string]JobSpec{
				"z-first":     {Run: "sleep 0.05; touch " + shellEscape(marker)},
				"a-dependent": {Needs: []string{"z-first"}, Run: "test -f " + shellEscape(marker)},
				"independent": {Run: "sleep 0.05"},
			}
			ctx := context.Background()
			if _, err := runtime.executeJobs(ctx, resource, jobs, nil, true); err != nil {
				t.Fatal(err)
			}
			results, _ := runtime.service.Store.ListWorkflowJobResults(ctx, runtime.revision.ID)
			byName := map[string]core.WorkflowJobResult{}
			for _, result := range results {
				byName[result.JobName] = result
				concurrent := 0
				for _, other := range results {
					if !other.StartedAt.After(*result.StartedAt) && other.FinishedAt.After(*result.StartedAt) {
						concurrent++
					}
				}
				if concurrent > limit {
					t.Fatalf("running %d jobs with a limit of %d", concurrent, limit)
				}
			}
			if byName["a-dependent"].StartedAt.Before(*byName["z-first"].FinishedAt) {
				t.Fatal("dependent job started before its dependency finished")
			}
		})
	}
}

func TestParallelFailureCancelsSiblingAndRunsFinally(t *testing.T) {
	runtime, resource := schedulerFixture(t)
	runtime.maxParallelJobs = 2
	barrier := filepath.Join(t.TempDir(), "running")
	cleanup := filepath.Join(t.TempDir(), "cleanup")
	dependent := filepath.Join(t.TempDir(), "dependent")
	jobs := map[string]JobSpec{
		"a-failure":   {Run: "while [ ! -f " + shellEscape(barrier) + " ]; do sleep 0.01; done; exit 17"},
		"b-running":   {Run: "touch " + shellEscape(barrier) + "; sleep 100"},
		"c-dependent": {Needs: []string{"a-failure"}, Run: "touch " + shellEscape(dependent)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := runtime.executeJobs(ctx, resource, jobs, map[string]JobSpec{"cleanup": {Run: "touch " + shellEscape(cleanup)}}, true)
	if err == nil || !strings.Contains(err.Error(), "job a-failure") {
		t.Fatalf("original failure was lost: %v", err)
	}
	if _, err := os.Stat(cleanup); err != nil {
		t.Fatal("finally job did not run", err)
	}
	if _, err := os.Stat(dependent); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dependent job ran after failure")
	}
	results, _ := runtime.service.Store.ListWorkflowJobResults(context.Background(), runtime.revision.ID)
	for _, result := range results {
		if result.JobName == "b-running" && result.State != "cancelled" {
			t.Fatalf("sibling was not cancelled: %s", result.State)
		}
	}
}

func TestParallelJobsRespectExternalCancellation(t *testing.T) {
	runtime, resource := schedulerFixture(t)
	runtime.maxParallelJobs = 2
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := runtime.executeJobs(ctx, resource, map[string]JobSpec{"a": {Run: "sleep 100"}, "b": {Run: "sleep 100"}}, nil, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation was lost: %v", err)
	}
}

func TestParseJobScheduling(t *testing.T) {
	for _, item := range []struct{ fields, jobs, want string }{
		{fields: "maxParallelJobs: 3", jobs: "a: {run: true}\n    b: {run: true, needs: [a]}"},
		{fields: "maxParallelJobs: 33", jobs: "a: {run: true}", want: "maxParallelJobs"},
		{fields: "maxParallelJobs: -1", jobs: "a: {run: true}", want: "maxParallelJobs"},
		{jobs: "a: {run: true, needs: [missing]}", want: "unknown"},
		{jobs: "a: {run: true, needs: [a]}", want: "cycle"},
		{jobs: "a: {run: true, needs: [b]}\n    b: {run: true, needs: [a]}", want: "cycle"},
		{jobs: "a: {run: true}\n    b: {run: true, needs: [a, a]}", want: "duplicate"},
	} {
		doc := fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: Pipeline\nmetadata: {name: test}\nspec:\n  %s\n  jobs:\n    %s\n", item.fields, item.jobs)
		_, err := Parse("pipeline.yaml", []byte(doc))
		if item.want == "" && err != nil || item.want != "" && (err == nil || !strings.Contains(err.Error(), item.want)) {
			t.Fatalf("unexpected validation for %s: %v", doc, err)
		}
	}
}

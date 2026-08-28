package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryCacheReusesMirrorAndIsolatesWorktrees(t *testing.T) {
	ctx := context.Background()
	remote := filepath.Join(t.TempDir(), "remote.git")
	working := filepath.Join(t.TempDir(), "working")
	environment := append(os.Environ(),
		"GIT_AUTHOR_NAME=Dispatch Test", "GIT_AUTHOR_EMAIL=dispatch@example.test",
		"GIT_COMMITTER_NAME=Dispatch Test", "GIT_COMMITTER_EMAIL=dispatch@example.test")
	if err := runGit(ctx, environment, "init", "--bare", remote); err != nil {
		t.Fatal(err)
	}
	if err := runGit(ctx, environment, "init", "--initial-branch=main", working); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(working, "revision.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", working, "add", "revision.txt"}, {"-C", working, "commit", "-m", "first"}, {"-C", working, "remote", "add", "origin", remote}, {"-C", working, "push", "origin", "main"}} {
		if err := runGit(ctx, environment, args...); err != nil {
			t.Fatal(err)
		}
	}
	firstCommit := gitOutput(t, working, "rev-parse", "HEAD")
	cache := newRepositoryCache(t.TempDir())
	firstPath := filepath.Join(t.TempDir(), "first")
	first, err := cache.checkout(ctx, remote, "connection-a", "main", firstCommit, firstPath, environment)
	if err != nil {
		t.Fatal(err)
	}
	defer first.remove(ctx)

	if err := os.WriteFile(filepath.Join(working, "revision.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", working, "add", "revision.txt"}, {"-C", working, "commit", "-m", "second"}, {"-C", working, "push", "origin", "main"}} {
		if err := runGit(ctx, environment, args...); err != nil {
			t.Fatal(err)
		}
	}
	secondCommit := gitOutput(t, working, "rev-parse", "HEAD")
	secondPath := filepath.Join(t.TempDir(), "second")
	second, err := cache.checkout(ctx, remote, "connection-a", "main", secondCommit, secondPath, environment)
	if err != nil {
		t.Fatal(err)
	}
	defer second.remove(ctx)

	assertFileContents(t, filepath.Join(firstPath, "revision.txt"), "one")
	assertFileContents(t, filepath.Join(secondPath, "revision.txt"), "two")
	if firstPath == secondPath {
		t.Fatal("cache returned one mutable worktree for two runs")
	}
	mirrors, err := filepath.Glob(filepath.Join(cache.root, "mirrors", "*.git"))
	if err != nil || len(mirrors) != 1 {
		t.Fatalf("expected one shared mirror, got %v (err=%v)", mirrors, err)
	}
}

func TestRepositoryCacheKeyIncludesCredentialsAndCanonicalizesURL(t *testing.T) {
	first := repositoryCacheKey("HTTPS://GitHub.Example/Owner/Repo.git/", "connection-a")
	second := repositoryCacheKey("https://github.example/Owner/Repo", "connection-a")
	if first != second {
		t.Fatalf("equivalent repository URLs produced different keys: %s %s", first, second)
	}
	if first == repositoryCacheKey("https://github.example/Owner/Repo", "connection-b") {
		t.Fatal("different credentials shared a repository cache key")
	}
}

func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := append([]string{"-C", directory}, args...)
	contents, err := runGitOutput(command...)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func runGitOutput(args ...string) (string, error) {
	command := exec.Command("git", args...)
	output, err := command.Output()
	return strings.TrimSpace(string(output)), err
}

func assertFileContents(t *testing.T, path, expected string) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != expected {
		t.Fatalf("%s = %q, want %q", path, contents, expected)
	}
}

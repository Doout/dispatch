package workflow

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
)

func TestHostedRepositoryCacheIgnoresControllerGitCredentials(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	fixtureRoot := t.TempDir()
	fixtureEnvironment := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + fixtureRoot,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	command := func(environment []string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), git, args...)
		cmd.Env = environment
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
		return strings.TrimSpace(string(output))
	}
	repositories := filepath.Join(fixtureRoot, "repositories")
	repository := filepath.Join(repositories, "source.git")
	command(fixtureEnvironment, "init", "--initial-branch=main", repository)
	if err = os.WriteFile(filepath.Join(repository, "message.txt"), []byte("accepted tenant content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(fixtureEnvironment, "-C", repository, "add", "message.txt")
	command(fixtureEnvironment, "-C", repository, "-c", "user.name=Tenant Test", "-c", "user.email=tenant@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Accepted source")
	accepted := command(fixtureEnvironment, "-C", repository, "rev-parse", "HEAD")
	if err = os.WriteFile(filepath.Join(repository, "message.txt"), []byte("later tenant content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(fixtureEnvironment, "-C", repository, "add", "message.txt")
	command(fixtureEnvironment, "-C", repository, "-c", "user.name=Tenant Test", "-c", "user.email=tenant@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Later source")
	latest := command(fixtureEnvironment, "-C", repository, "rev-parse", "HEAD")

	const tenantHeader = "Bearer tenant-source-token"
	var requestMu sync.Mutex
	var requestHeaders [][]string
	backend := &cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repositories, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers := append([]string(nil), r.Header.Values("Authorization")...)
		requestMu.Lock()
		requestHeaders = append(requestHeaders, headers)
		requestMu.Unlock()
		if len(headers) != 1 || headers[0] != tenantHeader {
			http.Error(w, "expected only the tenant credential", http.StatusForbidden)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	repositoryURL := server.URL + "/source.git"
	authority := filepath.Join(fixtureRoot, "ca.pem")
	if err = os.WriteFile(authority, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}

	controller := filepath.Join(fixtureRoot, "controller")
	command(fixtureEnvironment, "init", "--initial-branch=main", controller)
	command(fixtureEnvironment, "-C", controller, "config", "--local", "http.extraHeader", "Authorization: Bearer controller-private-token")
	command(fixtureEnvironment, "-C", controller, "remote", "add", "origin", "https://controller.invalid/private.git")
	t.Chdir(controller)
	environment, cleanup, err := deploy.PrepareIsolatedGitEnvironment(core.App{SourceAuthType: deploy.SourceAuthGitHubToken, SourceCredential: "tenant-source-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	environment = append(environment, "GIT_SSL_CAINFO="+authority)
	remoteHead := command(environment, "ls-remote", "--exit-code", repositoryURL, "refs/heads/main")
	if remoteHead != latest+"\trefs/heads/main" {
		t.Fatalf("tenant repository head: %q", remoteHead)
	}

	cache := newRepositoryCache(filepath.Join(fixtureRoot, "cache"))
	worktree, err := cache.checkout(t.Context(), repositoryURL, "tenant-source", "main", accepted, filepath.Join(fixtureRoot, "checkout"), environment)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := worktree.remove(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	assertFileContents(t, filepath.Join(worktree.path, "message.txt"), "accepted tenant content\n")
	if origin := command(environment, "--git-dir", worktree.mirrorPath, "config", "--local", "--get", "remote.origin.url"); origin != repositoryURL {
		t.Fatalf("cache origin = %q, want %q", origin, repositoryURL)
	}
	if config := command(environment, "--git-dir", worktree.mirrorPath, "config", "--local", "--list"); strings.Contains(config, "controller-private-token") || strings.Contains(config, "tenant-source-token") {
		t.Fatalf("source credential persisted in cache configuration: %s", config)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	if len(requestHeaders) < 2 {
		t.Fatalf("expected source discovery and fetch requests, got %d", len(requestHeaders))
	}
	for _, headers := range requestHeaders {
		if len(headers) != 1 || headers[0] != tenantHeader {
			t.Fatalf("unexpected Git request credentials: %q", headers)
		}
	}
}

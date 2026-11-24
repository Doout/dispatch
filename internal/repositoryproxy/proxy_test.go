package repositoryproxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
)

func TestGitUsesSavedProxyForURLAndNamedRemote(t *testing.T) {
	// An empty dumb-HTTP repository is sufficient for ls-remote. Distinct
	// refs let us tell which route Git actually used.
	repositoryServer := func(ref string) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/org/repo.git/info/refs":
				fmt.Fprintf(w, "%s\trefs/heads/%s\n", strings.Repeat("a", 40), ref)
			case "/org/repo.git/HEAD":
				fmt.Fprintf(w, "ref: refs/heads/%s\n", ref)
			default:
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(server.Close)
		return server
	}
	direct, proxy := repositoryServer("direct"), repositoryServer("proxy")
	url := direct.URL + "/org/repo.git"
	root := t.TempDir()
	env := []string{"HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "no_proxy=*", "GIT_TERMINAL_PROMPT=0"}
	git := func(t *testing.T, environment []string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir, cmd.Env = root, environment
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", args[0], err, output)
		}
		return string(output)
	}
	git(t, env, "init", "--bare", "--quiet", root)
	git(t, env, "remote", "add", "origin", url)
	git(t, env, "config", "--global", "http."+url+".proxy", "")
	git(t, env, "config", "remote.origin.proxy", "")
	for _, tc := range []struct {
		name, remote, bypass, want string
	}{
		{"raw URL", url, "", "proxy"},
		{"named remote", "origin", "", "proxy"},
		{"saved bypass", "origin", "127.0.0.1", "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := git(t, GitEnvironment(env, url, proxy.URL, tc.bypass), "ls-remote", tc.remote)
			if !strings.Contains(output, "refs/heads/"+tc.want) {
				t.Fatalf("expected %s route, got %q", tc.want, output)
			}
		})
	}
}

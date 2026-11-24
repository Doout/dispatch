package main

import (
	"bytes"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClonePreviewSources(t *testing.T) {
	repo := t.TempDir()
	gitFixture(t, repo, "init", "-q", "-b", "main")
	if err := os.Mkdir(filepath.Join(repo, "chart"), 0755); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(repo, "chart", "Chart.yaml"), "name: demo\n", 0644)
	gitFixture(t, repo, "add", ".")
	gitFixture(t, repo, "commit", "-qm", "slot revision")
	base := gitFixture(t, repo, "rev-parse", "HEAD")
	gitFixture(t, repo, "switch", "-qc", "feature")
	gitFixture(t, repo, "commit", "--allow-empty", "-qm", "preview revision")
	pr := gitFixture(t, repo, "rev-parse", "HEAD")
	root := t.TempDir()
	gitFixture(t, root, "clone", "--bare", repo, "example/hello.git")
	remote := filepath.Join(root, "example", "hello.git")
	gitFixture(t, remote, "update-ref", "refs/pull/42/head", pr)
	gitFixture(t, remote, "update-server-info")
	server := httptest.NewTLSServer(http.FileServer(http.Dir(root)))
	defer server.Close()
	certFile := filepath.Join(t.TempDir(), "ca.pem")
	put(t, certFile, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})), 0644)
	t.Setenv("GIT_SSL_CAINFO", certFile)

	example, err := os.ReadFile("../../examples/doodship/preview.yaml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := strings.Replace(string(example), "sources: [hello]", "sources: [hello, sibling]", 1)
	workflow = strings.Replace(workflow, "    hello: {repository:", "    sibling: {repository: example/hello, branch: main}\n    hello: {repository:", 1)
	path := filepath.Join(t.TempDir(), "preview.yaml")
	put(t, path, workflow, 0644)
	args := []string{"workflow", "run", "--file", path, "--clone", "https", "--git-host", strings.TrimPrefix(server.URL, "https://"), "--pr", "42", "--dry-run"}
	var out bytes.Buffer
	if err := run(t.Context(), args, &out, &out); err == nil || !strings.Contains(err.Error(), "--pr-source") {
		t.Fatalf("ambiguous PR repository: %v", err)
	}
	if err := run(t.Context(), append(args, "--pr-source", "hello"), &out, &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"Source hello: " + pr + " (refs/pull/42/head)", "Source sibling: " + base + " (main)"} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("expected %q in %s", line, &out)
		}
	}
	out.Reset()
	if err := run(t.Context(), append(args, "--source", "hello="+repo), &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Source hello: "+pr+" (feature)") {
		t.Fatalf("explicit checkout did not supply the PR: %s", &out)
	}

	// A missing PR ref must fail instead of deploying the configured branch.
	if err := run(t.Context(), append(args, "--pr", "999", "--pr-source", "hello"), &out, &out); err == nil || !strings.Contains(err.Error(), "refs/pull/999/head") {
		t.Fatalf("missing PR ref: %v", err)
	}
}

func TestCloneURLs(t *testing.T) {
	for _, tc := range []struct{ repository, host, protocol, want string }{
		{"Watson/service", "github.ibm.com", "ssh", "ssh://git@github.ibm.com/Watson/service.git"},
		{"git@other.example:Watson/service.git", "github.ibm.com", "https", "https://other.example/Watson/service.git"},
		{"https://other.example/Watson/service", "", "ssh", "ssh://git@other.example/Watson/service.git"},
		{"Watson/service", "localhost:3000", "http", "http://localhost:3000/Watson/service.git"},
		{"Watson/service", "", "https", "https://github.com/Watson/service.git"},
	} {
		url, err := cloneURL(tc.repository, tc.host, tc.protocol)
		if err != nil || url != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.repository, url, err, tc.want)
		}
	}
	for _, repo := range []string{"file:///tmp/repo", "../repo", "https://user:password@example.org/a/b"} {
		if _, err := cloneURL(repo, "", "ssh"); err == nil {
			t.Errorf("accepted %q", repo)
		}
	}
}

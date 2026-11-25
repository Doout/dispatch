package workflowrunner

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestWorkerRepositoryRejectsLocalPathsAndGitHelpers(t *testing.T) {
	for _, value := range []string{"https://github.example.test/team/app.git", "ssh://git@github.example.test/team/app.git", "git@github.example.test:team/app.git", "https://[2001:db8::1]/team/app.git", "ssh://git@[2001:db8::1]:2222/team/app.git"} {
		if !ValidRepositoryURL(value) {
			t.Fatal("remote repository rejected", value)
		}
	}
	for _, value := range []string{"/home/worker/repo", "file:///home/worker/repo", "ext::echo@command", "-oProxyCommand=x@host:repo", "git@-oProxyCommand=x:repo", "ssh://-oProxyCommand=x@host/repo", "https://user:password@host/repo", "git@host:repo\ncommand", "https://host/repo?token=value", "https://host/repo#fragment", "http://host/repo"} {
		if ValidRepositoryURL(value) {
			t.Fatal("unsafe repository accepted", value)
		}
	}
}

func TestNamedRepositoryWorkerRequestRequiresScopedCheckoutSupport(t *testing.T) {
	r := testRequest()
	pat := "test-personal-access-token"
	source := Source{Revision: core.WorkflowSourceRevision{RepositoryConnectionID: "named"}, Credential: pat,
		ProxyURL: "https://user:proxy-pass@proxy.example.test:3129"}
	r.Workflow.Sources["gitops"] = source
	if err := r.Validate(); err == nil {
		t.Fatal("a named repository was accepted under the old protocol")
	}
	r.Version = r.RequiredVersion()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + pat))
	for _, secret := range []string{pat, encoded, source.ProxyURL, "proxy-pass"} {
		if strings.Contains(r.Redact("git request used "+secret), secret) {
			t.Fatal("worker log exposed a repository credential")
		}
	}
}

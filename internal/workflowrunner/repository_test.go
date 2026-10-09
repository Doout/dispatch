package workflowrunner

import "testing"

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

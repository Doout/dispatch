package workflow

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func TestWorkerChecksOutAcceptedSourceFromLocalHTTPSRepository(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required")
	}
	repositories := t.TempDir()
	repository := filepath.Join(repositories, "source.git")
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(git, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	command("init", "--initial-branch=main", repository)
	if err = os.WriteFile(filepath.Join(repository, "message.txt"), []byte("accepted-content"), 0600); err != nil {
		t.Fatal(err)
	}
	command("-C", repository, "add", "message.txt")
	command("-C", repository, "-c", "user.name=Worker Test", "-c", "user.email=worker@example.invalid", "commit", "-m", "Accepted source")
	accepted := command("-C", repository, "rev-parse", "HEAD")
	if err = os.WriteFile(filepath.Join(repository, "message.txt"), []byte("unaccepted-content"), 0600); err != nil {
		t.Fatal(err)
	}
	command("-C", repository, "add", "message.txt")
	command("-C", repository, "-c", "user.name=Worker Test", "-c", "user.email=worker@example.invalid", "commit", "-m", "Later source")
	server := httptest.NewTLSServer(&cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + repositories, "GIT_HTTP_EXPORT_ALL=1"}})
	defer server.Close()
	authority := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(authority, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSL_CAINFO", authority)
	raw, _ := json.Marshal(JobSpec{RunFrom: "source", Run: "cat message.txt; printf 'commit=%s\\n' \"$DISPATCH_SOURCE_SOURCE_COMMIT\" > \"$DISPATCH_OUTPUT_FILE\"", Outputs: []string{"commit"}})
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "resource", RevisionID: "revision", Mode: "tenant", Workflow: &workflowrunner.Workflow{Job: raw, Sources: map[string]workflowrunner.Source{"source": {URL: server.URL + "/source.git", Revision: core.WorkflowSourceRevision{Repository: "local/source", Branch: "main", CommitSHA: accepted}}}}}
	workspace := t.TempDir()
	if err = os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	result := ExecuteWorkerJob(context.Background(), request, workspace, func(string) {})
	if result.State != "succeeded" || result.Outputs["commit"] != accepted || !strings.Contains(result.Log, "accepted-content") || strings.Contains(result.Log, "unaccepted-content") {
		t.Fatalf("immutable worker checkout: %+v", result)
	}
}

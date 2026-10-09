package workflow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

type remoteJobStub struct{ request workflowrunner.Request }

func (s *remoteJobStub) Run(_ context.Context, r workflowrunner.Request, progress func(string)) (workflowrunner.Result, error) {
	s.request = r
	progress("worker output")
	return workflowrunner.Result{State: "succeeded", Log: "worker output", Outputs: map[string]string{"image": "built"}}, nil
}
func TestHostedJobDoesNotExecuteOnController(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "controller-command")
	runtime := jobRuntime{service: &Service{RequireRemote: true}, root: t.TempDir()}
	job := JobSpec{WorkerMode: "managed", Run: "touch '" + marker + "'"}
	if _, _, err := runtime.runJobCommand(context.Background(), job, nil, func(string) {}); err == nil {
		t.Fatal("hosted job fell back to local execution")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("controller command ran")
	}
	runner := &remoteJobStub{}
	runtime.service.RemoteJobs = runner
	runtime.workerSources = map[string]workerSource{}
	runtime.source = core.ConfigSource{ProjectID: "project"}
	runtime.revision = core.WorkflowRevision{ID: "revision", ResourceID: "resource"}
	out, log, err := runtime.runJobCommand(context.Background(), job, map[string]string{"TOKEN": "secret"}, func(string) {})
	if err != nil || out["image"] != "built" || log != "worker output" {
		t.Fatalf("remote execution: %v %v", out, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("controller executed a delegated command")
	}
	if runner.request.Mode != "managed" || runner.request.ProjectID != "project" || runner.request.Workflow.Secrets["TOKEN"] != "secret" {
		t.Fatal("remote request lost ownership or scoped credentials")
	}
}
func TestWorkerJobStreamsRedactedOutputBeforeCompletion(t *testing.T) {
	raw, _ := json.Marshal(JobSpec{Run: "printf 'started\\n'; sleep 2; printf '%s\\n' \"$TOKEN\"; printf 'image=built\\n' > \"$DISPATCH_OUTPUT_FILE\"", Outputs: []string{"image"}})
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "resource", RevisionID: "revision", Mode: "tenant", Workflow: &workflowrunner.Workflow{Job: raw, Sources: map[string]workflowrunner.Source{}, Secrets: map[string]string{"TOKEN": "private-token"}}}
	var mu sync.Mutex
	logs := []string{}
	live := make(chan struct{}, 1)
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	done := make(chan workflowrunner.Result, 1)
	go func() {
		done <- ExecuteWorkerJob(context.Background(), request, workspace, func(log string) {
			mu.Lock()
			logs = append(logs, log)
			mu.Unlock()
			if strings.Contains(log, "started") {
				select {
				case live <- struct{}{}:
				default:
				}
			}
		})
	}()
	select {
	case <-live:
	case result := <-done:
		t.Fatalf("job completed before live output: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("no live output")
	}
	result := <-done
	if result.State != "succeeded" || result.Outputs["image"] != "built" || !strings.Contains(result.Log, "[redacted]") {
		t.Fatalf("unexpected result: %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, log := range append(logs, result.Log) {
		if strings.Contains(log, "private-token") {
			t.Fatal("secret reached worker log")
		}
	}
}
func TestHostedRepositoryLocations(t *testing.T) {
	for _, value := range []string{"file:///etc", "/tmp/repo", "ext::sh -c id", "ssh://", "https://"} {
		if remoteWorkerSourceURL(value) {
			t.Errorf("unsafe repository accepted: %s", value)
		}
	}
	for _, value := range []string{"https://git.example/owner/repository.git", "ssh://git@git.example/owner/repository.git", "git@git.example:owner/repository.git"} {
		if !remoteWorkerSourceURL(value) {
			t.Errorf("remote repository rejected: %s", value)
		}
	}
}
func TestManagedWorkerRejectsRepositoryAndBuilder(t *testing.T) {
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "resource", RevisionID: "revision", Mode: "managed", Workflow: &workflowrunner.Workflow{Job: json.RawMessage(`{"run":"true","builder":"docker"}`), Sources: map[string]workflowrunner.Source{}}}
	if result := ExecuteWorkerJob(context.Background(), request, t.TempDir(), func(string) {}); result.State != "failed" {
		t.Fatal("managed Docker build was accepted")
	}
	request.Workflow.Sources["source"] = workflowrunner.Source{}
	if request.Validate() == nil {
		t.Fatal("managed source network was accepted")
	}
}

func TestManagedJobValidationRejectsRepositoryAndDockerAccess(t *testing.T) {
	job := JobSpec{WorkerMode: "managed", Run: "printf ready"}
	if err := validateJobs("jobs", map[string]JobSpec{"check": job}, nil, nil, false); err != nil {
		t.Fatal("source-free managed job rejected", err)
	}
	for _, invalid := range []JobSpec{{WorkerMode: "managed", Run: "true", Builder: "docker"}, {WorkerMode: "managed", Run: "true", RunFrom: "source"}, {WorkerMode: "managed", Run: "true", Sources: []string{"source"}}, {WorkerMode: "unknown", Run: "true"}} {
		if err := validateJobs("jobs", map[string]JobSpec{"check": invalid}, map[string]SourceSpec{"source": {Repository: "owner/repository"}}, nil, true); err == nil {
			t.Fatalf("unsafe managed configuration accepted: %+v", invalid)
		}
	}
	runtime := jobRuntime{service: &Service{}, root: t.TempDir()}
	if _, _, err := runtime.runJobCommand(context.Background(), job, nil, func(string) {}); err == nil {
		t.Fatal("managed selection fell back to self-hosted shell")
	}
}

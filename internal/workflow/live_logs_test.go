package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runlog"
)

func TestJobLogsAvailableBeforeExit(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime := &jobRuntime{root: root, paths: map[string]string{"service": root}, revision: core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{"service": {Alias: "service"}}}}
	job := JobSpec{RunFrom: "service", Run: `printf 'building\n'; printf '%s' "$TOKEN"; printf '\n'; while [ ! -f release ]; do sleep 0.1; done; printf 'done\n'`}
	seen := false
	_, log, err := runtime.runJobCommand(ctx, job, map[string]string{"TOKEN": "private-value"}, func(log string) {
		if strings.Contains(log, "private-value") {
			t.Error("secret exposed in live log")
		}
		if !strings.Contains(log, "building") {
			return
		}
		seen = true
		if err := os.WriteFile(filepath.Join(root, "release"), nil, 0600); err != nil {
			t.Error(err)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !seen || !strings.Contains(log, "done") || !strings.Contains(log, "[redacted]") {
		t.Fatalf("missing live/final log: %q", log)
	}
}

func TestFullJobArchiveContinuesAfterTheLocalViewerLimit(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "logs")
	archive, err := runlog.Open(directory, "job", "private-value")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &jobRuntime{root: root, paths: map[string]string{"service": root}, revision: core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{"service": {Alias: "service"}}}, logArchive: archive}
	job := JobSpec{RunFrom: "service", Run: `i=0; while [ "$i" -lt 25000 ]; do printf 'building image layer with more than forty characters for testing\n'; i=$((i+1)); done; printf '%s\n' "$TOKEN"`}
	_, preview, err := runtime.runJobCommand(context.Background(), job, map[string]string{"TOKEN": "private-value"}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview, "[log truncated]") {
		t.Fatal("test did not exceed the local viewer limit")
	}
	var full strings.Builder
	var offset int64
	for {
		text, next, incomplete, err := runlog.Read(directory, "job", offset)
		if err != nil || incomplete {
			t.Fatalf("full log failed: %v", err)
		}
		full.WriteString(text)
		if next == offset {
			break
		}
		offset = next
	}
	if strings.Count(full.String(), "building image layer") != 25000 || strings.Contains(full.String(), "private-value") || !strings.Contains(full.String(), "[redacted]") {
		t.Fatal("full command output missing or unsafe")
	}
}

func TestWorkerFullLogsUseTheExistingProgressChannel(t *testing.T) {
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	runner := &remoteJobStub{}
	runtime := jobRuntime{service: &Service{RemoteJobs: runner}, source: core.ConfigSource{ProjectID: "project"}, revision: core.WorkflowRevision{ID: "revision", ResourceID: "resource"}, workerSources: map[string]workerSource{}, fullLogID: "job"}
	job := JobSpec{Run: `i=0; while [ "$i" -lt 25000 ]; do printf 'building image layer with more than forty characters for testing\n'; i=$((i+1)); done; printf '%s\n' "$TOKEN"`}
	if _, _, err := runtime.runRemoteJob(context.Background(), job, map[string]string{"TOKEN": "private-value"}, func(string) {}); err != nil {
		t.Fatal(err)
	}
	if runner.request.LogID() != "job" {
		t.Fatal("controller did not enable log delivery in the worker request")
	}
	var full strings.Builder
	result := ExecuteWorkerJob(context.Background(), runner.request, workspace, func(chunk string) {
		full.WriteString(chunk)
	})
	if result.State != "succeeded" || !result.LogCaptured || !strings.Contains(result.Log, "[log truncated]") {
		t.Fatalf("worker did not capture the full output: %+v", result)
	}
	if strings.Count(full.String(), "building image layer") != 25000 || strings.Contains(full.String(), "private-value") || !strings.Contains(full.String(), "[redacted]") {
		t.Fatal("progress channel lost full command output or exposed a credential")
	}
}

func TestRedactPartialLogSecrets(t *testing.T) {
	secrets := map[string]string{"TOKEN": "private-value"}
	for _, log := range []string{"output private-", "output private-value", "output private-\n[log truncated]"} {
		if got := redactJobLog(log, secrets); strings.Contains(got, "private") {
			t.Fatalf("secret fragment exposed: %q", got)
		}
	}
}

func TestCancelledJobStopsShellChildren(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime := &jobRuntime{root: root, paths: map[string]string{"service": root}, revision: core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{"service": {Alias: "service"}}}}
	finished := make(chan error, 1)
	go func() {
		_, _, err := runtime.runJobCommand(ctx, JobSpec{RunFrom: "service", Run: "(sleep 1; touch finished) & wait"}, nil, func(string) {})
		finished <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled job succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled job did not stop")
	}
	time.Sleep(1100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(root, "finished")); !os.IsNotExist(err) {
		t.Fatalf("shell child continued after cancellation: %v", err)
	}
}

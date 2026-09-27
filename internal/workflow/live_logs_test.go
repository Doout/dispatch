package workflow

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

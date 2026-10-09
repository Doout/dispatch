package workflowrunner

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDisposableManagedWorkerIntegration(t *testing.T) {
	image := os.Getenv("DISPATCH_WORKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_WORKER_TEST_IMAGE to a built, pinned worker image")
	}
	executor := ContainerExecutor{Image: image, Mode: "managed", MemoryMiB: 256, DiskMiB: 64, PIDs: 32, CPUs: 0.5}
	request := testRequest()
	request.Mode = "managed"
	request.Workflow.Secrets = map[string]string{"TOKEN": "integration-worker-private-value"}
	request.Workflow.Job = json.RawMessage(`{"run":"test ! -e /var/run/docker.sock; test ! -e /var/lib/dispatch-worker; test -z \"${DISPATCH_EDGE_TOKEN:-}\"; test -z \"${DISPATCH_MASTER_KEY_FILE:-}\"; test $(id -u) = 65532; ! touch /forbidden 2>/dev/null; printf 'running\\n'; sleep 2; printf '%s\\n' \"$TOKEN\"; printf 'result=isolated\\n' > \"$DISPATCH_OUTPUT_FILE\"","outputs":["result"]}`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	live := make(chan string, 8)
	done := make(chan Result, 1)
	go func() {
		done <- executor.Execute(ctx, request, "", func(log string) {
			select {
			case live <- log:
			default:
			}
		})
	}()
	sawLive := false
	for {
		select {
		case log := <-live:
			if strings.Contains(log, request.Workflow.Secrets["TOKEN"]) {
				t.Fatal("unredacted live output")
			}
			if strings.Contains(log, "running") {
				sawLive = true
			}
		case result := <-done:
			if result.State != "succeeded" || result.Outputs["result"] != "isolated" || !sawLive || strings.Contains(result.Log, request.Workflow.Secrets["TOKEN"]) {
				t.Fatalf("managed worker result: %+v live=%v", result, sawLive)
			}
			return
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestDisposableWorkerCancellationIntegration(t *testing.T) {
	image := os.Getenv("DISPATCH_WORKER_TEST_IMAGE")
	if image == "" {
		t.Skip("set DISPATCH_WORKER_TEST_IMAGE to a built, pinned worker image")
	}
	executor := ContainerExecutor{Image: image, Mode: "managed", MemoryMiB: 256, DiskMiB: 64, PIDs: 32, CPUs: 0.5}
	request := testRequest()
	request.Mode = "managed"
	request.Workflow.Job = json.RawMessage(`{"run":"printf 'started\\n'; sleep 120"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		done <- executor.Execute(ctx, request, "", func(log string) {
			if strings.Contains(log, "started") {
				cancel()
			}
		})
	}()
	select {
	case result := <-done:
		if result.State != "cancelled" {
			t.Fatalf("cancellation result: %+v", result)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

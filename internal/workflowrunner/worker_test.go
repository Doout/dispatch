package workflowrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testRequest() Request {
	return Request{Version: Version, ProjectID: "project", ResourceID: "resource", RevisionID: "revision", Mode: "tenant", Workflow: &Workflow{Job: json.RawMessage(`{"run":"true"}`), Sources: map[string]Source{}}}
}
func TestWorkerReceiptsDoNotRepeatInterruptedOrCompletedOperations(t *testing.T) {
	var executions atomic.Int32
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	execute := func(context.Context, Request, string, func(string)) Result {
		executions.Add(1)
		return Result{State: "succeeded", Log: "finished"}
	}
	worker, err := OpenWorker(directory, "node", execute)
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest()
	encoded, _ := json.Marshal(request)
	digest := sha256.Sum256(encoded)
	job := LeasedJob{ID: "operation", Digest: hex.EncodeToString(digest[:]), Attempt: 1, Request: request}
	if result := worker.Run(context.Background(), job, nil); result.State != "succeeded" {
		t.Fatalf("execution failed: %+v", result)
	}
	changed := job
	changed.Request.Workflow = &Workflow{Job: json.RawMessage(`{"run":"changed"}`)}
	if result := worker.Run(context.Background(), changed, nil); result.State != "unknown" {
		t.Fatal("changed command reused receipt")
	}
	worker.Close()
	worker, err = OpenWorker(directory, "node", execute)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	job.Attempt = 2
	if result := worker.Run(context.Background(), job, nil); result.State != "succeeded" || result.Log != "finished" {
		t.Fatalf("completed receipt lost: %+v", result)
	}
	if executions.Load() != 1 {
		t.Fatal("completed command was repeated")
	}
	raw, _ := json.Marshal(workerReceipt{Digest: job.Digest, State: "running"})
	if err = os.WriteFile(filepath.Join(directory, "interrupted.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	job.ID = "interrupted"
	if result := worker.Run(context.Background(), job, nil); result.State != "unknown" {
		t.Fatal("interrupted command was repeated")
	}
	job.ID = "missing"
	if result := worker.Run(context.Background(), job, nil); result.State != "unknown" {
		t.Fatal("missing receipt was replayed")
	}
	if executions.Load() != 1 {
		t.Fatal("unproven command was repeated")
	}
	if _, err = OpenWorker(directory, "node", execute); err == nil {
		t.Fatal("parallel worker ownership allowed")
	}
}
func TestContainerLimitsDoNotDependOnRequest(t *testing.T) {
	executor := ContainerExecutor{Image: "sha256:" + strings.Repeat("a", 64), Mode: "managed", MemoryMiB: 128, DiskMiB: 64, PIDs: 32, CPUs: 0.5}
	if err := executor.Validate(); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(executor.arguments("job"), " ")
	for _, expected := range []string{"--network none", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges", "--pids-limit 32", "--memory 128m", "--cpus 0.50", "--user 65532:65532"} {
		if !strings.Contains(args, expected) {
			t.Errorf("missing sandbox restriction %s", expected)
		}
	}
	if strings.Contains(args, "docker.sock") || strings.Contains(args, "--privileged") {
		t.Fatal("managed job gained host privileges")
	}
	executor.AllowDocker = true
	if executor.Validate() == nil {
		t.Fatal("managed Docker mount accepted")
	}
	executor.AllowDocker = false
	executor.Network = "host"
	if executor.Validate() == nil {
		t.Fatal("managed host network accepted")
	}
}

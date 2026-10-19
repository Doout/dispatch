package workflowrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/runlog"
)

func TestFullWorkerLogsSurviveReceiptReplayWithoutRepeatingTheBuild(t *testing.T) {
	directory := t.TempDir()
	_ = os.Chmod(directory, 0700)
	executions := 0
	worker, err := OpenWorker(directory, "node", func(ctx context.Context, r Request, _ string, progress func(string)) Result {
		executions++
		stream := runlog.Stream(func(text string) error { progress(text); return nil }, r.LogSecrets()...)
		for i := 0; i < 1000; i++ {
			_, _ = stream.Write([]byte(strings.Repeat("building image ", 100) + "private-value\n"))
		}
		return Result{State: "succeeded", LogCaptured: stream.Close() == nil}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	r := testRequest()
	r.Workflow.Job = json.RawMessage(`{"run":"true","dispatchLogId":"full-log"}`)
	r.Workflow.Secrets = map[string]string{"TOKEN": "private-value"}
	raw, _ := json.Marshal(r)
	digest := sha256.Sum256(raw)
	j := LeasedJob{ID: "operation", Attempt: 1, Request: r, Digest: hex.EncodeToString(digest[:])}
	result := worker.Run(context.Background(), j, nil)
	if result.State != "succeeded" || result.LogSize == nil || *result.LogSize < 1<<20 {
		t.Fatalf("full capture missing: %+v", result)
	}
	var offset int64
	var full strings.Builder
	for {
		text, next := worker.logPage(j.ID, offset)
		full.WriteString(text)
		if next == offset {
			break
		}
		offset = next
	}
	if strings.Contains(full.String(), "private-value") || offset != *result.LogSize {
		t.Fatal("worker log changed or leaked a secret")
	}
	j.Attempt = 2
	replayed := worker.Run(context.Background(), j, nil)
	if executions != 1 || replayed.LogSize == nil || *replayed.LogSize != offset {
		t.Fatal("receipt replay lost the archive or repeated execution")
	}
	if !runlog.Complete(filepath.Join(directory, "github-logs"), j.ID) {
		t.Fatal("missing completion receipt")
	}
}

func TestOlderExecutionImageCannotMarkMissingLogsComplete(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	worker, err := OpenWorker(directory, "node", func(context.Context, Request, string, func(string)) Result {
		return Result{State: "succeeded"}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	r := testRequest()
	r.Workflow.Job = json.RawMessage(`{"run":"true","dispatchLogId":"build-log"}`)
	raw, _ := json.Marshal(r)
	digest := sha256.Sum256(raw)
	job := LeasedJob{ID: "operation", Attempt: 1, Request: r, Digest: hex.EncodeToString(digest[:])}
	result := worker.Run(context.Background(), job, nil)
	if result.State != "succeeded" || result.LogSize != nil || runlog.Complete(filepath.Join(directory, "github-logs"), job.ID) {
		t.Fatalf("missing log capture changed the build result or claimed complete logs: %+v", result)
	}
}

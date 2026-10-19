package workflowrunner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/edgeclient"
	"github.com/doout/dispatch/internal/runlog"
)

func TestFullWorkerClientTransfersLargeLogsBeforeAcknowledgingCompletion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := testRequest()
	r.Workflow.LogID = "build-log"
	r.Workflow.Secrets = map[string]string{"TOKEN": "private-value"}
	raw, _ := json.Marshal(r)
	digest := sha256.Sum256(raw)
	job := LeasedJob{ID: "operation", Digest: hex.EncodeToString(digest[:]), Attempt: 1, LeaseToken: "lease", ExpiresAt: time.Now().Add(time.Minute), Request: r}
	controllerLogs := filepath.Join(t.TempDir(), "logs")
	var completed Completion
	chunks := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer session" {
			t.Error("missing worker identity")
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/next"):
			_ = json.NewEncoder(w).Encode(job)
		case strings.HasSuffix(request.URL.Path, "/heartbeat"):
			var p Progress
			if err := json.NewDecoder(request.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			if p.LogChunk != "" {
				chunks++
				if err := runlog.Receive(controllerLogs, r.Workflow.LogID, p.LogOffset, p.LogChunk, r.LogSecrets()...); err != nil {
					t.Error(err)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"cancelRequested": false, "logOffset": runlog.Position(controllerLogs, r.Workflow.LogID)})
		case strings.HasSuffix(request.URL.Path, "/complete"):
			if err := json.NewDecoder(request.Body).Decode(&completed); err != nil {
				t.Error(err)
			}
			if completed.Result.LogSize == nil || completed.Result.LogIncomplete {
				t.Error("completed without a full log receipt")
			} else if err := runlog.Finish(controllerLogs, r.Workflow.LogID, *completed.Result.LogSize); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"accepted": true})
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()
	directory := t.TempDir()
	_ = os.Chmod(directory, 0700)
	worker, err := OpenWorker(directory, "node", func(ctx context.Context, r Request, _ string, _ func(string)) Result {
		stream := runlog.Stream(func(text string) error { return PublishLogChunk(ctx, text) }, r.LogSecrets()...)
		for i := 0; i < 800; i++ {
			_, _ = stream.Write([]byte(strings.Repeat("building image ", 100) + "private-value\n"))
		}
		return Result{State: "succeeded", LogCaptured: stream.Close() == nil}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	client := Client{HTTP: server.Client(), Identity: edgeclient.Identity{NodeID: "node", Controller: server.URL}, Mode: "tenant", session: edgeclient.Session{Token: "session", ExpiresAt: time.Now().Add(time.Hour)}}
	found, err := client.Poll(ctx, worker)
	if err != nil || !found || completed.Result.State != "succeeded" || chunks < 8 || !runlog.Complete(controllerLogs, r.Workflow.LogID) {
		t.Fatalf("full worker transfer failed: %v %d %+v", err, chunks, completed.Result)
	}
	var offset int64
	var full strings.Builder
	for {
		text, next, incomplete, err := runlog.Read(controllerLogs, r.Workflow.LogID, offset)
		if err != nil || incomplete {
			t.Fatal("received log is incomplete")
		}
		full.WriteString(text)
		if next == offset {
			break
		}
		offset = next
	}
	if full.Len() < 1<<20 || strings.Contains(full.String(), "private-value") || strings.Count(full.String(), "building image ") != 80000 {
		t.Fatal("received full log changed or exposed a credential")
	}
}

package workflowrunner

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/runlog"
	"github.com/doout/dispatch/internal/store"
)

func newTestBroker(t *testing.T, ctx context.Context) (*Broker, *store.SQLStore) {
	t.Helper()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key")
	if err = os.WriteFile(key, []byte(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	return &Broker{Store: data, Vault: vault, LogDirectory: filepath.Join(t.TempDir(), "logs"), Authorize: func(context.Context, Request) error { return nil }}, data
}

func enrollTestWorker(t *testing.T, ctx context.Context, data *store.SQLStore, id, version string) core.PrivateNetwork {
	t.Helper()
	now := time.Now().UTC()
	node := core.PrivateNetwork{ID: id, Name: id, Driver: "dispatch_agent", Config: map[string]string{"workflowMode": "tenant"}, Details: map[string]string{"workerVersion": version, "workerCheckedAt": now.Format(time.RFC3339Nano)}, CreatedAt: now, UpdatedAt: now}
	if err := data.CreatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: node.ID, EnrollmentHash: "token", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.EnrollEdgeCredential(ctx, node.ID, "token", "public-key", "session", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestStatefulWorkerOperationReusesReceiptAndRejectsChangedInputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	broker, data := newTestBroker(t, ctx)
	node := enrollTestWorker(t, ctx, data, "node", Version)
	var err error
	request := testRequest()
	request.Workflow.ServiceRunID = "accepted-service-run"
	request.Workflow.Job = json.RawMessage(`{"run":"true","dispatchLogId":"log-archive"}`)
	done := make(chan error, 1)
	go func() { _, err := broker.Run(ctx, request, nil); done <- err }()
	var job *LeasedJob
	for job == nil {
		job, err = broker.Lease(ctx, node.ID, 1, "tenant", Version)
		if err != nil {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if job == nil {
			time.Sleep(10 * time.Millisecond)
		}
	}
	chunk := "building image\n"
	if _, err := broker.Renew(ctx, node.ID, job.ID, Progress{LeaseToken: "wrong", LogChunk: chunk}); err == nil {
		t.Fatal("stale lease appended logs")
	}
	if runlog.Position(broker.LogDirectory, request.LogID()) != 0 {
		t.Fatal("stale lease changed log cursor")
	}
	reply, err := broker.Renew(ctx, node.ID, job.ID, Progress{LeaseToken: job.LeaseToken, LogChunk: chunk})
	if err != nil || reply.CancelRequested || reply.LogOffset == nil || *reply.LogOffset != int64(len(chunk)) {
		t.Fatalf("accepted chunk not acknowledged: %+v %v", reply, err)
	}
	size := int64(len(chunk))
	if err := broker.Complete(ctx, node.ID, job.ID, Completion{LeaseToken: "wrong", Result: Result{State: "succeeded", LogSize: &size}}); err == nil {
		t.Fatal("stale lease completed log delivery")
	}
	if runlog.Complete(broker.LogDirectory, request.LogID()) {
		t.Fatal("unaccepted completion marked logs complete")
	}
	if err = broker.Complete(ctx, node.ID, job.ID, Completion{LeaseToken: job.LeaseToken, Result: Result{State: "succeeded", LogSize: &size, Outputs: map[string]string{"password": "saved-password"}}}); err != nil {
		t.Fatal(err)
	}
	if !runlog.Complete(broker.LogDirectory, request.LogID()) {
		t.Fatal("accepted full log transfer missing")
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	result, err := broker.Run(ctx, request, nil)
	if err != nil || result.Outputs["password"] != "saved-password" {
		t.Fatalf("receipt lost: %+v %v", result, err)
	}
	request.Workflow.Inputs = map[string]string{"password": "regenerated-password"}
	if _, err = broker.Run(ctx, request, nil); err == nil || !strings.Contains(err.Error(), "different inputs") {
		t.Fatal("changed inputs reused accepted operation", err)
	}
	if job, err = broker.Lease(ctx, node.ID, 1, "tenant", Version); err != nil || job != nil {
		t.Fatal("replay queued another command", err)
	}
}

func TestMixedFleetLeasesRepositoryWorkOnlyToCompatibleWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	broker, data := newTestBroker(t, ctx)
	legacy := enrollTestWorker(t, ctx, data, "legacy", Version)
	request := testRequest()
	request.Workflow.ServiceRunID = "repository-run"
	request.Workflow.Sources["gitops"] = Source{Revision: core.WorkflowSourceRevision{RepositoryConnectionID: "gitops"}}
	request.Version = request.RequiredVersion()
	if _, err := broker.Run(ctx, request, nil); err == nil || !strings.Contains(err.Error(), "upgraded") {
		t.Fatalf("legacy-only fleet accepted named workflow or gave no upgrade guidance: %v", err)
	}
	modern := enrollTestWorker(t, ctx, data, "modern", RepositoryAccessVersion)
	deployment := Request{Mode: "tenant", ProjectID: request.ProjectID, Deployment: &Deployment{SourceProxyURL: "https://proxy.example.test"}}
	deployment.Version = deployment.RequiredVersion()
	if nodeAllows(legacy, deployment) || !nodeAllows(modern, deployment) || !nodeAllows(modern, testRequest()) {
		t.Fatal("worker routing lost proxied deployments or compatibility with v1 jobs")
	}

	for _, tc := range []struct {
		id, pollVersion string
	}{{"current-worker", RepositoryAccessVersion}, {"downgraded-worker", Version}} {
		t.Run(tc.id, func(t *testing.T) {
			request.Workflow.ServiceRunID = tc.id
			done := make(chan error, 1)
			go func() { _, err := broker.Run(ctx, request, nil); done <- err }()
			for {
				scheduled, err := data.GetWorkflowWorkerJob(ctx, operationID(request))
				if err == nil {
					if scheduled.NodeID != modern.ID {
						t.Fatalf("v2 job assigned to %s", scheduled.NodeID)
					}
					break
				}
				if !errors.Is(err, store.ErrNotFound) {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					t.Fatalf("job finished before being scheduled: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			job, err := broker.Lease(ctx, legacy.ID, 1, "tenant", Version)
			if err != nil || job != nil {
				t.Fatal("legacy node could lease v2 job", err)
			}
			job, err = broker.Lease(ctx, modern.ID, 1, "tenant", tc.pollVersion)
			if tc.pollVersion == Version {
				if !errors.Is(err, store.ErrWorkerLease) || job != nil {
					t.Fatal("a downgraded worker could lease v2 work", err)
				}
				if err := <-done; err == nil || !strings.Contains(err.Error(), "protocol changed") {
					t.Fatal("downgrade did not report a protocol change", err)
				}
				return
			}
			if err != nil || job == nil || job.Request.Version != RepositoryAccessVersion {
				t.Fatal("current worker failed to lease named checkout", err)
			}
			if err := broker.Complete(ctx, modern.ID, job.ID, Completion{LeaseToken: job.LeaseToken, Result: Result{State: "succeeded"}}); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

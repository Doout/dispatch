package workflowrunner

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
)

func TestStatefulWorkerOperationReusesReceiptAndRejectsChangedInputs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "worker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
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
	now := time.Now().UTC()
	node := core.PrivateNetwork{ID: "node", Name: "Node", Driver: "dispatch_agent", Config: map[string]string{"workflowMode": "tenant"}, Details: map[string]string{"workerVersion": Version, "workerCheckedAt": now.Format(time.RFC3339Nano)}, CreatedAt: now, UpdatedAt: now}
	if err = data.CreatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err = data.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: node.ID, EnrollmentHash: "token", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = data.EnrollEdgeCredential(ctx, node.ID, "token", "public-key", "session", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	broker := Broker{Store: data, Vault: vault, Authorize: func(context.Context, Request) error { return nil }}
	request := testRequest()
	request.Workflow.ServiceRunID = "accepted-service-run"
	done := make(chan error, 1)
	go func() { _, err := broker.Run(ctx, request, nil); done <- err }()
	var job *LeasedJob
	for job == nil {
		job, err = broker.Lease(ctx, node.ID, 1, "tenant")
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
	if err = broker.Complete(ctx, node.ID, job.ID, Completion{LeaseToken: job.LeaseToken, Result: Result{State: "succeeded", Outputs: map[string]string{"password": "saved-password"}}}); err != nil {
		t.Fatal(err)
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
	if job, err = broker.Lease(ctx, node.ID, 1, "tenant"); err != nil || job != nil {
		t.Fatal("replay queued another command", err)
	}
}

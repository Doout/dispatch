package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/edgeclient"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimeclient"
	"github.com/doout/dispatch/internal/store"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/go-chi/chi/v5"
)

func TestEnrolledWorkflowWorkerStreamsAndCompletesEncryptedJob(t *testing.T) {
	a := serviceTestAPI(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	node := core.PrivateNetwork{ID: "workflow-node", Name: "Worker", Driver: edge.DriverAgent, Config: map[string]string{"workflowMode": "tenant", "workflowProjectId": "project"}, State: "ready", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := a.store.CreatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	credentials, _ := a.edgeCredentials()
	token, err := edge.RotateCredentials(ctx, credentials, node.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	broker := &workflowrunner.Broker{Store: a.store.(store.WorkflowWorkerStore), Vault: a.eventConfig.Vault, Authorize: func(_ context.Context, r workflowrunner.Request) error { checks.Add(1); return nil }}
	routes := chi.NewRouter()
	routes.Route("/api/v1", func(r chi.Router) { a.MountWorkflowRunnerRoutes(r, broker) })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/workflow/jobs/") {
			routes.ServeHTTP(w, r)
		} else {
			a.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	identity, err := edgeclient.LoadIdentity(filepath.Join(t.TempDir(), "identity.json"), server.URL, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := workflowrunner.Client{HTTP: server.Client(), Identity: identity, Enrollment: token, Mode: "tenant"}
	state := filepath.Join(t.TempDir(), "state")
	continueJob := make(chan struct{})
	worker, err := workflowrunner.OpenWorker(state, node.ID, func(ctx context.Context, r workflowrunner.Request, _ string, progress func(string)) workflowrunner.Result {
		progress("running " + r.Workflow.Secrets["TOKEN"])
		select {
		case <-ctx.Done():
			return workflowrunner.Result{State: "cancelled"}
		case <-continueJob:
		}
		return workflowrunner.Result{State: "succeeded", Log: "finished", Outputs: map[string]string{"value": "result"}}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); worker.Close() }()
	if busy, err := client.Poll(ctx, worker); err != nil || busy {
		t.Fatalf("worker enrollment/poll: %v %v", busy, err)
	}
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "resource", RevisionID: "revision", Mode: "tenant", Workflow: &workflowrunner.Workflow{Job: json.RawMessage(`{"run":"true"}`), Secrets: map[string]string{"TOKEN": "private-job-token"}}}
	done := make(chan error, 1)
	live := make(chan string, 2)
	go func() {
		out, err := broker.Run(ctx, request, func(log string) {
			select {
			case live <- log:
			default:
			}
		})
		if err == nil && out.Outputs["value"] != "result" {
			err = store.ErrWorkerLease
		}
		done <- err
	}()
	workerDone := make(chan error, 1)
	go func() {
		for {
			busy, err := client.Poll(ctx, worker)
			if err != nil || busy {
				workerDone <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case log := <-live:
		if strings.Contains(log, "private-job-token") || !strings.Contains(log, "[REDACTED]") {
			t.Fatalf("unexpected live log %q", log)
		}
	case err := <-done:
		t.Fatalf("completed before live logs: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(continueJob)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
	if checks.Load() < 4 {
		t.Fatal("worker authority was not checked at lease and completion")
	}
	// The same enrolled identity can also advertise and poll the typed runtime
	// queue, preserving storage/backup/rollback without a competing agent session.
	typed := runtimeclient.Client{HTTP: server.Client(), Controller: server.URL, Node: node.ID, Token: client.Token}
	if job, err := typed.Lease(ctx); err != nil || job != nil {
		t.Fatalf("shared typed runtime poll: %v %v", job, err)
	}
	registered, err := a.store.GetPrivateNetwork(ctx, node.ID)
	if err != nil || registered.Details["runtimeVersion"] != remoteruntime.APIVersion || registered.Details["workerVersion"] != workflowrunner.Version {
		t.Fatal("worker lost negotiated runtime capabilities", err)
	}
	// Durable local receipts contain encrypted output, not secret values.
	files, _ := os.ReadDir(state)
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".json") {
			raw, _ := os.ReadFile(filepath.Join(state, file.Name()))
			if bytes.Contains(raw, []byte("private-job-token")) {
				t.Fatal("plaintext secret persisted in worker receipt")
			}
		}
	}
}
func TestWorkflowWorkerRejectsLegacySessionAndModeEscalation(t *testing.T) {
	a, node, session, _ := runtimeAPIFixture(t)
	node.Config = map[string]string{"workflowMode": "managed"}
	if err := a.store.UpdatePrivateNetwork(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	broker := &workflowrunner.Broker{Store: a.store.(store.WorkflowWorkerStore), Vault: a.eventConfig.Vault, Authorize: func(context.Context, workflowrunner.Request) error { return nil }}
	routes := chi.NewRouter()
	routes.Route("/api/v1", func(r chi.Router) { a.MountWorkflowRunnerRoutes(r, broker) })
	for _, test := range []struct {
		token, mode string
		status      int
	}{{node.EnrollmentToken, "managed", 401}, {session.Token, "tenant", 422}, {session.Token, "managed", 204}} {
		request := httptest.NewRequest("GET", "/api/v1/edge/nodes/"+node.ID+"/workflow/jobs/next", nil)
		request.Header.Set("Authorization", "Bearer "+test.token)
		request.Header.Set("X-Dispatch-Worker-Version", workflowrunner.Version)
		request.Header.Set("X-Dispatch-Worker-Mode", test.mode)
		response := httptest.NewRecorder()
		routes.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("mode %s: %d %s", test.mode, response.Code, response.Body.String())
		}
	}
	other := serviceTestAPI(t)
	otherRoutes := chi.NewRouter()
	otherRoutes.Route("/api/v1", func(r chi.Router) { other.MountWorkflowRunnerRoutes(r, broker) })
	request := httptest.NewRequest("GET", "/api/v1/edge/nodes/"+node.ID+"/workflow/jobs/next", nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	request.Header.Set("X-Dispatch-Worker-Version", workflowrunner.Version)
	request.Header.Set("X-Dispatch-Worker-Mode", "managed")
	response := httptest.NewRecorder()
	otherRoutes.ServeHTTP(response, request)
	if response.Code != 401 {
		t.Fatalf("worker crossed tenant store: %d", response.Code)
	}
}

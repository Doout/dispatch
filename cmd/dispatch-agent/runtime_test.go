package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/runtimecontract"
)

type runtimeWorkerFunc func(context.Context, remoteruntime.LeasedJob, deploy.Progress) remoteruntime.Result

func (f runtimeWorkerFunc) Run(ctx context.Context, j remoteruntime.LeasedJob, p deploy.Progress) remoteruntime.Result {
	return f(ctx, j, p)
}

func TestRuntimeLeaseLossCancelsExecutionBeforeAnotherJob(t *testing.T) {
	previous := runtimeHeartbeatInterval
	runtimeHeartbeatInterval = 5 * time.Millisecond
	defer func() { runtimeHeartbeatInterval = previous }()
	var completions atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/heartbeat") {
			completions.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	var stopped atomic.Bool
	worker := runtimeWorkerFunc(func(ctx context.Context, _ remoteruntime.LeasedJob, _ deploy.Progress) remoteruntime.Result {
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		stopped.Store(true)
		return remoteruntime.Result{State: "unknown", Code: runtimecontract.Uncertain}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := executeRuntime(ctx, server.Client(), server.URL, "node", func(context.Context) (string, error) { return "revoked", nil }, worker, remoteruntime.LeasedJob{ID: "job", LeaseToken: "lease", ExpiresAt: time.Now().Add(time.Minute)})
	if !errors.Is(err, errEdgeUnauthorized) || !stopped.Load() || completions.Load() != 0 {
		t.Fatalf("lost lease did not stop before return: %v stopped=%v completions=%d", err, stopped.Load(), completions.Load())
	}
}

func TestRuntimeCancellationRenewsSessionAndReportsUnknownReceipt(t *testing.T) {
	previous := runtimeHeartbeatInterval
	runtimeHeartbeatInterval = 5 * time.Millisecond
	defer func() { runtimeHeartbeatInterval = previous }()
	var tokens atomic.Int32
	var completed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dispatch-Runtime-Version") != remoteruntime.APIVersion || r.Header.Get("X-Dispatch-Runtime-Capabilities") == "" {
			t.Error("missing runtime negotiation")
		}
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			if r.Header.Get("Authorization") != "Bearer renewed-session" {
				t.Error("session refresh omitted")
			}
			_ = json.NewEncoder(w).Encode(map[string]bool{"cancelRequested": true})
			return
		}
		var input remoteruntime.Completion
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.LeaseToken != "lease" || input.Result.State != "unknown" {
			t.Error("lost cancellation outcome")
		}
		completed.Store(true)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	worker := runtimeWorkerFunc(func(ctx context.Context, _ remoteruntime.LeasedJob, _ deploy.Progress) remoteruntime.Result {
		<-ctx.Done()
		return remoteruntime.Result{State: "unknown", Code: runtimecontract.Uncertain}
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := executeRuntime(ctx, server.Client(), server.URL, "node", func(context.Context) (string, error) { tokens.Add(1); return "renewed-session", nil }, worker, remoteruntime.LeasedJob{ID: "job", LeaseToken: "lease", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || !completed.Load() || tokens.Load() < 2 {
		t.Fatalf("cancellation result not delivered: %v", err)
	}
}

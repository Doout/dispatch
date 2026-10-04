package automationclient

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

func promotedReadinessResponse() map[string]any {
	response := readinessResponse("ready")
	response["sourceSnapshotId"] = "snapshot-1"
	response["powerState"] = "running"
	response["promotionState"] = "promoted"
	response["promotionEvidence"] = map[string]any{
		"operationId": "original-promotion", "quarantineReleased": true,
		"copiedWorkloadsDisabled": true, "productionBindingsCleared": true,
	}
	return response
}

func TestServerReadinessAcceptsPromotedCloneOnlyWithCompleteEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"verified-promotion", func(r map[string]any) {}, true},
		{"promotion-still-pending", func(r map[string]any) { r["promotionState"] = "pending" }, false},
		{"missing-evidence", func(r map[string]any) { delete(r, "promotionEvidence") }, false},
		{"missing-promotion-operation", func(r map[string]any) { delete(r["promotionEvidence"].(map[string]any), "operationId") }, false},
		{"invalid-promotion-operation", func(r map[string]any) { r["promotionEvidence"].(map[string]any)["operationId"] = "../foreign" }, false},
		{"quarantine-retained", func(r map[string]any) { r["promotionEvidence"].(map[string]any)["quarantineReleased"] = false }, false},
		{"copied-workloads-enabled", func(r map[string]any) { r["promotionEvidence"].(map[string]any)["copiedWorkloadsDisabled"] = false }, false},
		{"production-bindings-retained", func(r map[string]any) { r["promotionEvidence"].(map[string]any)["productionBindingsCleared"] = false }, false},
		{"runtime-still-isolated", func(r map[string]any) { r["runtimeState"] = "verified-isolated" }, false},
		{"machine-stopped", func(r map[string]any) { r["powerState"] = "stopped" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v1/infrastructure/servers/server-1" {
					t.Error("readiness submitted a mutation or changed machine", r.Method, r.URL.Path)
				}
				body := promotedReadinessResponse()
				tc.change(body)
				json.NewEncoder(w).Encode(body)
			})
			for _, name := range []string{"server_get", "server_wait"} {
				out := c.Call(context.Background(), name, Arguments{ServerID: "server-1"})
				if out.OK != tc.valid || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.ResourceID != "server-1" {
					t.Fatal("promotion readiness changed original identity", name, out)
				}
				if tc.valid {
					// Readiness keeps the allocation operation as its continuation;
					// promotion evidence names its separate accepted operation.
					if out.Continuation.OperationID != "original-operation" || out.Continuation.ProjectID != "project-1" {
						t.Fatal("promotion discarded original readiness continuation", out)
					}
				} else if out.Error == nil || out.Error.Code != "invalid_response" {
					t.Fatal("incomplete promotion evidence declared deployable", name, out)
				}
			}
			if calls.Load() != 2 {
				t.Fatal("terminal readiness implicitly polled or mutated", calls.Load())
			}
		})
	}
}

func TestServerWaitStopsAtStoppedMachineWithoutStartingIt(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/v1/infrastructure/servers/server-1" {
			t.Error("stopped machine was implicitly started", r.Method, r.URL.Path)
		}
		body := readinessResponse("stopped")
		body["powerState"] = "stopped"
		json.NewEncoder(w).Encode(body)
	})
	out := c.Call(context.Background(), "server_wait", Arguments{ServerID: "server-1", TimeoutSeconds: 5})
	if out.OK || out.Error == nil || out.Error.Code != "operation_stopped" || out.ExitCode() != 5 || calls.Load() != 1 || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.OperationID != "original-operation" || len(out.Data) == 0 {
		t.Fatal("stopped wait polled, started work or lost recovery evidence", out, calls.Load())
	}
	out = c.Call(context.Background(), "server_get", Arguments{ServerID: "server-1"})
	if !out.OK || calls.Load() != 2 || out.Continuation == nil || out.Continuation.OperationID != "original-operation" {
		t.Fatal("stopped machine cannot be inspected", out, calls.Load())
	}
}

func TestServerReadinessRejectsContradictoryPowerAndIsolationStates(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		change      func(map[string]any)
	}{
		{"stopped-without-power-evidence", "stopped", func(r map[string]any) {}},
		{"stopped-with-running-power", "stopped", func(r map[string]any) { r["powerState"] = "running" }},
		{"stopped-deployable", "stopped", func(r map[string]any) { r["powerState"], r["deployable"] = "stopped", true }},
		{"isolated-already-promoted", "verified-isolated", func(r map[string]any) {
			r["sourceSnapshotId"], r["runtimeState"], r["promotionState"] = "snapshot-1", "verified-isolated", "promoted"
		}},
		{"isolated-but-stopped", "verified-isolated", func(r map[string]any) {
			r["sourceSnapshotId"], r["runtimeState"], r["powerState"] = "snapshot-1", "verified-isolated", "stopped"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				body := readinessResponse(tc.state)
				tc.change(body)
				json.NewEncoder(w).Encode(body)
			})
			out := c.Call(context.Background(), "server_wait", Arguments{ServerID: "server-1"})
			if out.OK || out.Error == nil || out.Error.Code != "invalid_response" || out.Continuation == nil || out.Continuation.ID != "server-1" {
				t.Fatal("contradictory readiness escaped or displaced original machine", out)
			}
		})
	}
}

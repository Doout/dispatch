package automationclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func readinessResponse(state string) map[string]any {
	return map[string]any{"id": "server-1", "projectId": "project-1", "allocationState": "allocated", "enrollmentState": "enrolled", "runtimeState": "ready", "waitState": state, "deployable": state == "ready", "latestOperation": map[string]any{"id": "original-operation", "serverId": "server-1", "state": "succeeded"}}
}

func TestServerWaitRequiresAuthenticatedRuntimeAndPreservesContinuation(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/infrastructure/servers/server-1" {
			t.Error("wait started a mutation or changed server", r.Method, r.URL.Path)
		}
		response := readinessResponse("waiting")
		response["enrollmentState"], response["runtimeState"] = "waiting", "waiting"
		if calls.Add(1) > 1 {
			response = readinessResponse("ready")
		}
		json.NewEncoder(w).Encode(response)
	})
	out := c.Call(context.Background(), "server_wait", Arguments{ServerID: "server-1", TimeoutSeconds: 5})
	if !out.OK || calls.Load() != 2 || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.OperationID != "original-operation" || out.Continuation.ProjectID != "project-1" || out.Continuation.ResourceID != "server-1" {
		t.Fatal("allocation or continuation was confused with readiness", out, calls.Load())
	}
	calls.Store(0)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	out = c.Call(ctx, "server_wait", Arguments{ServerID: "server-1", TimeoutSeconds: 5})
	if out.OK || out.ExitCode() != 4 || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.OperationID != "original-operation" || len(out.Data) == 0 || calls.Load() != 1 {
		t.Fatal("timeout lost original readiness evidence", out)
	}
}

func TestServerWaitStopsForIsolationApprovalAndRecovery(t *testing.T) {
	for _, tc := range []struct{ state, code string }{
		{"ready", ""}, {"verified-isolated", ""}, {"pending_approval", ""},
		{"failed", "operation_failed"}, {"expired", "operation_failed"}, {"revoked", "operation_failed"}, {"deleted", "operation_failed"},
		{"cancelled", "operation_cancelled"}, {"unknown", "outcome_unknown"}, {"unresolved", "outcome_unknown"},
		{"paused", "operation_paused"},
	} {
		t.Run(tc.state, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				response := readinessResponse(tc.state)
				if tc.state == "verified-isolated" {
					response["sourceSnapshotId"], response["runtimeState"] = "snapshot-1", "verified-isolated"
				}
				json.NewEncoder(w).Encode(response)
			})
			out := c.Call(context.Background(), "server_wait", Arguments{ServerID: "server-1", TimeoutSeconds: 1})
			if got := calls.Load(); got != 1 || out.OK != (tc.code == "") || out.Continuation == nil || out.Continuation.OperationID != "original-operation" || tc.code != "" && (out.Error == nil || out.Error.Code != tc.code) {
				t.Fatal("wrong readiness stop", out, got)
			}
			// Inspection succeeds even when the server needs recovery.
			if get := c.Call(context.Background(), "server_get", Arguments{ServerID: "server-1"}); !get.OK {
				t.Fatal("inspection hid recovery state", get)
			}
		})
	}
}

func TestServerReadinessRejectsWrongIdentityAndContradictoryEvidence(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(s map[string]any) { s["id"] = "foreign-server" },
		func(s map[string]any) { s["allocationState"] = "pending" },
		func(s map[string]any) { s["enrollmentState"] = "waiting" },
		func(s map[string]any) { s["runtimeState"] = "waiting" },
		func(s map[string]any) { s["sourceSnapshotId"] = "snapshot-1" },
		func(s map[string]any) { s["waitState"] = "future-state" },
		func(s map[string]any) {
			s["latestOperation"] = map[string]any{"id": "original-operation", "serverId": "foreign-server"}
		},
		func(s map[string]any) { delete(s, "deployable") },
	} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			s := readinessResponse("ready")
			change(s)
			json.NewEncoder(w).Encode(s)
		})
		out := c.Call(context.Background(), "server_wait", Arguments{ServerID: "server-1", TimeoutSeconds: 1})
		if out.OK || out.Error == nil || out.Error.Code != "invalid_response" || out.Continuation == nil || out.Continuation.ID != "server-1" {
			t.Fatal("invalid evidence escaped or lost server ID", out)
		}
	}
}

func TestServerWaitRechecksCurrentAccessOnEveryPoll(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 1 {
			w.WriteHeader(403)
			io.WriteString(w, `{"title":"Current project grant revoked"}`)
			return
		}
		json.NewEncoder(w).Encode(readinessResponse("waiting"))
	})
	out := c.Call(context.Background(), "server_wait", Arguments{ServerID: "server-1", TimeoutSeconds: 5})
	if out.OK || out.ExitCode() != 3 || calls.Load() != 2 || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.OperationID != "original-operation" {
		t.Fatal("wait ignored revocation or lost original operation", out)
	}
}

func TestServerReadinessMCPToolsAndContinuation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("MCP inspection changed infrastructure")
		}
		response := readinessResponse("verified-isolated")
		response["sourceSnapshotId"], response["runtimeState"] = "snapshot-1", "verified-isolated"
		json.NewEncoder(w).Encode(response)
	})
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer receive.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.ServeMCP(ctx, input, output); output.Close() }()
	enc, scan := json.NewEncoder(send), bufio.NewScanner(receive)
	read := func() map[string]json.RawMessage {
		t.Helper()
		if !scan.Scan() {
			t.Fatal("missing MCP response", scan.Err())
		}
		var result map[string]json.RawMessage
		if json.Unmarshal(scan.Bytes(), &result) != nil {
			t.Fatal("invalid MCP response")
		}
		return result
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}})
	read()
	enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	for i, name := range []string{"server_get", "server_wait"} {
		op, found := Find(name)
		if !found || toolDescription(op)["annotations"].(map[string]any)["readOnlyHint"] != true {
			t.Fatal("MCP readiness tool is missing or not read-only", name)
		}
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": i + 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": map[string]any{"serverId": "server-1"}}})
		var payload struct {
			StructuredContent Result `json:"structuredContent"`
		}
		if json.Unmarshal(read()["result"], &payload) != nil || !payload.StructuredContent.OK || payload.StructuredContent.Continuation == nil || payload.StructuredContent.Continuation.OperationID != "original-operation" {
			t.Fatal("MCP lost isolated server evidence", payload)
		}
	}
	send.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

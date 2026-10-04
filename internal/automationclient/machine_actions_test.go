package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
)

func machineActionReceipt(action string) map[string]any {
	return map[string]any{
		"id": "original-receipt", "projectId": "project-1", "action": action,
		"operationKind": "infrastructure_operation", "operationId": "original-operation",
		"resourceId": "server-1", "state": "accepted",
	}
}

func TestMachineActionsSendExplicitTypedInputAndPreserveReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, path, action, input string
	}{
		{"server_power", "power", "server.start", `{"action":"start","revision":7}`},
		{"server_power", "power", "server.stop", `{"action":"stop","revision":7}`},
		{"server_power", "power", "server.reboot", `{"action":"reboot","revision":7}`},
		{"clone_promote", "promote", "server.promote", `{"network":"workload-network","revision":7,"confirmName":"exact clone name"}`},
	} {
		t.Run(tc.action, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/api/v1/infrastructure/servers/server-1/"+tc.path || r.Header.Get("Idempotency-Key") != "original-request-key" {
					t.Error("changed original mutation", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				var want, got map[string]any
				if json.Unmarshal([]byte(tc.input), &want) != nil || json.NewDecoder(r.Body).Decode(&got) != nil || !reflect.DeepEqual(got, want) {
					t.Error("changed explicit machine action input", got, want)
				}
				w.Header().Set("Location", "/api/v1/mutation-receipts/original-receipt")
				w.Header().Set("Idempotency-Replayed", "true")
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(machineActionReceipt(tc.action))
			})
			out := c.Call(context.Background(), tc.name, Arguments{ServerID: "server-1", Key: "original-request-key", Input: json.RawMessage(tc.input)})
			want := &Continuation{Kind: "receipt", ID: "original-receipt", ResourceID: "server-1", OperationID: "original-operation", ProjectID: "project-1", Key: "original-request-key"}
			if !out.OK || !out.Replayed || calls.Load() != 1 || !reflect.DeepEqual(out.Continuation, want) {
				t.Fatal("lost durable receipt or implicitly replayed mutation", out, calls.Load())
			}
		})
	}
}

func TestMachineActionsRejectIncompleteOrInventedInputBeforeTransport(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, tc := range []struct{ name, input string }{
		{"server_power", `{"action":"start"}`},
		{"server_power", `{"revision":1}`},
		{"server_power", `{"action":"resize","revision":1}`},
		{"server_power", `{"action":"stop","revision":0}`},
		{"server_power", `{"action":"start","revision":1,"confirmName":"invented"}`},
		{"clone_promote", `{"network":"workload","revision":1}`},
		{"clone_promote", `{"network":"../foreign","revision":1,"confirmName":"clone"}`},
		{"clone_promote", `{"network":"workload","revision":1,"confirmName":" "}`},
		{"clone_promote", `{"network":"workload","revision":0,"confirmName":"clone"}`},
		{"clone_promote", `{"network":"workload","revision":1,"confirmName":"clone","verified":true}`},
	} {
		out := c.Call(context.Background(), tc.name, Arguments{ServerID: "server-1", Key: "original-request-key", Input: json.RawMessage(tc.input)})
		if out.OK || out.Error == nil || out.Error.Code != "invalid_input" {
			t.Fatal("invalid machine input reached transport", tc, out)
		}
	}
	for _, name := range []string{"server_power", "clone_promote"} {
		out := c.Call(context.Background(), name, Arguments{ServerID: "server-1", Input: json.RawMessage(`{}`)})
		if out.OK || out.Error == nil || out.Error.Code != "invalid_input" {
			t.Fatal("accepted mutation without original request key", out)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid input submitted a mutation", calls.Load())
	}
}

func TestMachineActionsRejectForeignOrIncompleteReceipts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		path   string
	}{
		{"foreign-resource", func(r map[string]any) { r["resourceId"] = "foreign-server" }, "original-receipt"},
		{"different-action", func(r map[string]any) { r["action"] = "server.start" }, "original-receipt"},
		{"different-kind", func(r map[string]any) { r["operationKind"] = "deployment" }, "original-receipt"},
		{"missing-receipt", func(r map[string]any) { delete(r, "id") }, "original-receipt"},
		{"missing-operation", func(r map[string]any) { delete(r, "operationId") }, "original-receipt"},
		{"invalid-operation", func(r map[string]any) { r["operationId"] = "../foreign" }, "original-receipt"},
		{"different-location", func(r map[string]any) {}, "foreign-receipt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				body := machineActionReceipt("server.stop")
				tc.change(body)
				w.Header().Set("Location", "/api/v1/mutation-receipts/"+tc.path)
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(body)
			})
			out := c.Call(context.Background(), "server_power", Arguments{ServerID: "server-1", Key: "original-request-key", Input: json.RawMessage(`{"action":"stop","revision":1}`)})
			if out.OK || out.Error == nil || out.Error.Code != "invalid_response" || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.Key != "original-request-key" || out.Continuation.OperationID != "" {
				t.Fatal("foreign receipt escaped or displaced original request", out)
			}
		})
	}
}

func TestMachineActionsValidateOriginalDirectAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any, map[string]any)
		valid  bool
	}{
		{"original", func(s, o map[string]any) {}, true},
		{"foreign-server", func(s, o map[string]any) { s["id"] = "foreign-server" }, false},
		{"foreign-operation-server", func(s, o map[string]any) { o["serverId"] = "foreign-server" }, false},
		{"foreign-provider-resource", func(s, o map[string]any) { o["resourceId"] = "foreign-machine" }, false},
		{"missing-provider-resource", func(s, o map[string]any) { delete(s, "resourceId"); delete(o, "resourceId") }, false},
		{"different-action", func(s, o map[string]any) { o["action"] = "server.stop" }, false},
		{"missing-operation", func(s, o map[string]any) { delete(o, "id") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				s := map[string]any{"id": "server-1", "projectId": "project-1", "resourceId": "provider-machine-1", "providerId": "provider-1"}
				o := map[string]any{"id": "original-operation", "serverId": "server-1", "action": "server.promote", "resourceId": "provider-machine-1", "providerId": "provider-1"}
				tc.change(s, o)
				w.WriteHeader(http.StatusAccepted)
				json.NewEncoder(w).Encode(map[string]any{"server": s, "operation": o})
			})
			out := c.Call(context.Background(), "clone_promote", Arguments{ServerID: "server-1", Key: "original-request-key", Input: json.RawMessage(`{"network":"workload","revision":1,"confirmName":"clone"}`)})
			if out.OK != tc.valid || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.Key != "original-request-key" {
				t.Fatal("direct acceptance changed original machine identity", out)
			}
			if tc.valid {
				if out.Continuation.OperationID != "original-operation" || out.Continuation.ProjectID != "project-1" {
					t.Fatal("direct acceptance lost original operation", out)
				}
			} else if out.Error == nil || out.Error.Code != "invalid_response" || out.Continuation.OperationID != "" {
				t.Fatal("foreign operation accepted for recovery", out)
			}
		})
	}
}

func TestMachineActionLostReplyDoesNotImplicitlyReplay(t *testing.T) {
	for _, tc := range []struct{ name, input string }{
		{"server_power", `{"action":"start","revision":1}`},
		{"clone_promote", `{"network":"workload","revision":1,"confirmName":"clone"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			})
			out := c.Call(context.Background(), tc.name, Arguments{ServerID: "server-1", Key: "original-request-key", Input: json.RawMessage(tc.input)})
			if out.OK || out.Error == nil || out.Error.Code != "unavailable" || calls.Load() != 1 || out.Continuation == nil || out.Continuation.Kind != "managed_server" || out.Continuation.ID != "server-1" || out.Continuation.ResourceID != "server-1" || out.Continuation.Key != "original-request-key" || out.Continuation.OperationID != "" {
				t.Fatal("lost reply replayed work or lost original request", out, calls.Load())
			}
		})
	}
}

func TestCloneInspectionKeepsOriginalMachineAndProviderResource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"verified", func(r map[string]any) {}, true},
		{"unverified", func(r map[string]any) { r["verified"] = false }, true},
		{"foreign-server", func(r map[string]any) { r["server"].(map[string]any)["id"] = "foreign-server" }, false},
		{"foreign-provider-resource", func(r map[string]any) { r["resource"].(map[string]any)["id"] = "foreign-machine" }, false},
		{"missing-provider-resource", func(r map[string]any) { delete(r, "resource") }, false},
		{"not-a-clone", func(r map[string]any) { delete(r["server"].(map[string]any), "sourceSnapshotId") }, false},
		{"missing-verification", func(r map[string]any) { delete(r, "verified") }, false},
		{"unsupported-integrity-claim", func(r map[string]any) { r["applicationIntegrity"] = "verified" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v1/infrastructure/servers/server-1/clone" {
					t.Error("clone inspection submitted work or changed machine", r.Method, r.URL.Path)
				}
				body := map[string]any{
					"server":   map[string]any{"id": "server-1", "projectId": "project-1", "resourceId": "provider-machine-1", "sourceSnapshotId": "snapshot-1"},
					"resource": map[string]any{"id": "provider-machine-1"}, "verified": true, "applicationIntegrity": "unverified",
				}
				tc.change(body)
				json.NewEncoder(w).Encode(body)
			})
			out := c.Call(context.Background(), "clone_inspect", Arguments{ServerID: "server-1"})
			if out.OK != tc.valid || calls.Load() != 1 || out.Continuation == nil || out.Continuation.ID != "server-1" || out.Continuation.ResourceID != "server-1" {
				t.Fatal("clone inspection changed original identity", out)
			}
			if !tc.valid && (out.Error == nil || out.Error.Code != "invalid_response") {
				t.Fatal("invalid clone evidence accepted", out)
			}
		})
	}
}

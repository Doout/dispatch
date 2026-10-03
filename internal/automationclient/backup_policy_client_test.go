package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBackupPolicyCommandsKeepReviewedRetentionAndActualReceipt(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workload-backup-policies" || r.Header.Get("Idempotency-Key") != "capture-policy-once" {
			t.Error("changed policy identity")
		}
		var input BackupPolicyCreateInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Name != "daily-postgres" || input.ConfirmRetention != input.Name || input.IntervalHours != 24 || input.KeepLast != 7 {
			t.Error("changed retention acceptance")
		}
		w.Header().Set("Location", "/api/v1/mutation-receipts/policy-receipt")
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(201)
		io.WriteString(w, `{"id":"policy-receipt","operationId":"policy-1","resourceId":"source-run","operationKind":"workload_backup_policy"}`)
	})
	args := Arguments{Key: "capture-policy-once", Input: json.RawMessage(`{"name":"daily-postgres","sourceRunId":"source-run","intervalHours":24,"keepLast":7,"confirmRetention":"daily-postgres"}`)}
	for range 2 {
		r := c.Call(context.Background(), "backup_policy_create", args)
		if !r.OK || !r.Replayed || r.Continuation == nil || r.Continuation.Kind != "receipt" || r.Continuation.ID != "policy-receipt" || r.Continuation.OperationID != "policy-1" || r.Continuation.Key != args.Key {
			t.Fatal("lost accepted policy identity", r)
		}
	}
}

func TestBackupPolicyInputsRequireExplicitRetentionAndPause(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, input := range []string{
		`{"name":"daily-pg","sourceRunId":"source-run","intervalHours":24,"keepLast":7}`,
		`{"name":"daily-pg","sourceRunId":"source-run","intervalHours":24,"keepLast":7,"confirmRetention":"different"}`,
		`{"name":"daily-pg","sourceRunId":"source-run","intervalHours":0,"keepLast":7,"confirmRetention":"daily-pg"}`,
		`{"name":"daily-pg","sourceRunId":"source-run","intervalHours":24,"keepLast":0,"confirmRetention":"daily-pg"}`,
		`{"name":"daily-pg","sourceRunId":"source-run","intervalHours":24,"keepLast":7,"confirmRetention":"daily-pg","checks":[{"query":"SELECT 1"}]}`,
	} {
		r := c.Call(context.Background(), "backup_policy_create", Arguments{Key: "capture-policy-once", Input: json.RawMessage(input)})
		if r.OK || r.ExitCode() != 2 {
			t.Fatal("accepted implicit retention policy", r)
		}
	}
	for _, input := range []string{`{"revision":1,"confirmName":"daily-pg"}`, `{"revision":1,"enabled":null,"confirmName":"daily-pg"}`, `{"revision":1,"enabled":false}`, `{"revision":1,"enabled":true,"confirmName":"daily-pg","keepLast":1}`} {
		r := c.Call(context.Background(), "backup_policy_set", Arguments{PolicyID: "policy-1", Input: json.RawMessage(input)})
		if r.OK || r.ExitCode() != 2 {
			t.Fatal("accepted implicit pause or altered retention", r)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid policy reached server")
	}
}

func TestBackupPolicyInspectionAndCASPreservePolicyContinuation(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/workload-backup-policies" {
			if r.URL.Query().Get("projectId") != "project-1" {
				t.Error("lost project filter")
			}
			io.WriteString(w, `[]`)
			return
		}
		if r.URL.Path != "/api/v1/workload-backup-policies/policy-1" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("changed policy or invented receipt")
		}
		if r.Method == "PUT" {
			var input BackupPolicyUpdateInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.Enabled == nil || *input.Enabled || input.Revision != 2 || input.ConfirmName != "daily-pg" {
				t.Error("pause was not explicit")
			}
		}
		io.WriteString(w, `{"id":"policy-1","enabled":false,"revision":3,"lastVerifiedBackupId":"usable-archive"}`)
	})
	if r := c.Call(context.Background(), "backup_policies_list", Arguments{ProjectID: "project-1"}); !r.OK {
		t.Fatal(r)
	}
	for _, name := range []string{"backup_policy_get", "backup_policy_set"} {
		args := Arguments{PolicyID: "policy-1"}
		if name == "backup_policy_set" {
			args.Input = json.RawMessage(`{"revision":2,"enabled":false,"confirmName":"daily-pg"}`)
		}
		r := c.Call(context.Background(), name, args)
		if !r.OK || r.Continuation == nil || r.Continuation.ID != "policy-1" || r.Continuation.Kind != "workload_backup_policy" || !strings.Contains(string(r.Data), "usable-archive") {
			t.Fatal("policy evidence lost", r)
		}
	}
	op, _ := Find("backup_policy_set")
	schema := toolDescription(op)["inputSchema"].(map[string]any)["properties"].(map[string]any)["input"].(map[string]any)
	if !strings.Contains(string(recoveryJSON(schema["required"])), `"enabled"`) {
		t.Fatal("MCP pause schema does not require enabled")
	}
}

package automationclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

var recoveryPolicy = core.RetentionPolicy{ProjectID: "project-1", LogDays: 7, RunDays: 30, KeepRuns: 10, ImageDays: 7, StoppedRevisionDays: 7, KeepRollbackRevisions: 5}

func recoveryJSON(v any) json.RawMessage { raw, _ := json.Marshal(v); return raw }
func confirmation(id, action string) json.RawMessage {
	return recoveryJSON(RecoveryConfirmation{ResourceConfirmation{ResourceID: id, Action: action, ExpectedVersion: "reviewed-version", ConfirmName: "operator-supplied-name"}})
}
func TestRecoveryRoutesKeepExactInputsAndActualContinuations(t *testing.T) {
	cases := []struct {
		name, path, body, kind, id, operation string
		args                                  Arguments
	}{
		{"service_get", "/service-provision-runs/run-1/resource", `{"runId":"run-1","operationId":"original","state":"unresolved"}`, "service_resource", "run-1", "original", Arguments{RunID: "run-1"}},
		{"service_inspect", "/service-provision-runs/run-1/resource/inspect", `{"runId":"run-1","state":"absent"}`, "service_resource", "run-1", "", Arguments{RunID: "run-1"}},
		{"service_reconcile", "/service-provision-runs/run-1/resource/reconcile", `{"runId":"run-1","operationId":"original","state":"recovering"}`, "service_resource", "run-1", "original", Arguments{RunID: "run-1"}},
		{"service_retry", "/service-provision-runs/run-1/resource/retry", `{"runId":"run-1","operationId":"original","state":"recovering"}`, "service_resource", "run-1", "original", Arguments{RunID: "run-1"}},
		{"service_delete", "/service-provision-runs/run-1/resource/delete", `{"id":"receipt-1","operationKind":"service_resource","operationId":"cleanup-1","resourceId":"run-1"}`, "receipt", "receipt-1", "cleanup-1", Arguments{RunID: "run-1", Key: "stable-delete-key", Input: confirmation("run-1", "delete")}},
		{"retention_review", "/projects/project-1/retention/preview", `{"runtime":{"id":"review-1","projectId":"project-1","state":"planned"}}`, "runtime_retention_review", "review-1", "", Arguments{ProjectID: "project-1", Input: recoveryJSON(RuntimeRetentionReviewInput{Scope: "runtime", ExpectedPolicy: &recoveryPolicy})}},
		{"retention_apply", "/projects/project-1/retention/apply", `{"applied":true,"runtime":{"id":"review-1","projectId":"project-1","state":"partial"}}`, "runtime_retention_review", "review-1", "", Arguments{ProjectID: "project-1", Input: recoveryJSON(RuntimeRetentionApplyInput{Scope: "runtime", ExpectedPolicy: &recoveryPolicy, Confirm: "project-1", RuntimeReviewID: "review-1", RuntimeReviewDigest: "exact-digest"})}},
		{"retention_get", "/projects/project-1/retention/runtime-reviews/review-1", `{"id":"review-1","projectId":"project-1","state":"partial"}`, "runtime_retention_review", "review-1", "", Arguments{ProjectID: "project-1", ReviewID: "review-1"}},
		{"backup_create", "/workload-backups", `{"id":"receipt-1","operationKind":"workload_backup","operationId":"backup-1","resourceId":"run-1"}`, "receipt", "receipt-1", "backup-1", Arguments{Key: "stable-backup-key", Input: recoveryJSON(WorkloadBackupCreateInput{SourceRunID: "run-1", Checks: []core.BackupIntegrityCheck{{Query: "SELECT count(*) FROM fixture", Expected: "1"}}})}},
		{"backup_get", "/workload-backups/backup-1", `{"id":"backup-1","state":"ready"}`, "workload_backup", "backup-1", "", Arguments{BackupID: "backup-1"}},
		{"backup_verify", "/workload-backups/backup-1/verify", `{"id":"verify-1","backupId":"backup-1","state":"running"}`, "workload_backup_operation", "verify-1", "", Arguments{BackupID: "backup-1", Key: "stable-verify-key"}},
		{"backup_reconcile", "/workload-backups/backup-1/operations/verify-1/reconcile", `{"id":"verify-1","backupId":"backup-1","state":"unresolved"}`, "workload_backup_operation", "verify-1", "", Arguments{BackupID: "backup-1", OperationID: "verify-1"}},
		{"backup_operation_get", "/workload-backup-operations/verify-1", `{"id":"verify-1","backupId":"backup-1","state":"unresolved"}`, "workload_backup_operation", "verify-1", "", Arguments{OperationID: "verify-1"}},
		{"backup_delete", "/workload-backups/backup-1/delete", `{"id":"delete-1","backupId":"backup-1","state":"running"}`, "workload_backup_operation", "delete-1", "", Arguments{BackupID: "backup-1", Key: "stable-delete-key", Input: confirmation("backup-1", "delete")}},
		{"backup_restore", "/workload-backups/backup-1/restore/destination-1", `{"id":"receipt-1","operationKind":"workload_backup","operationId":"restore-1","resourceId":"backup-1"}`, "receipt", "receipt-1", "restore-1", Arguments{BackupID: "backup-1", DestinationID: "destination-1", Key: "stable-restore-key", Input: confirmation("backup-1", "restore")}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				op, _ := Find(test.name)
				if r.Method != op.Method || r.URL.Path != "/api/v1"+test.path || r.Header.Get("Idempotency-Key") != test.args.Key {
					t.Errorf("changed request identity: %s %s key%q", r.Method, r.URL.Path, r.Header.Get("Idempotency-Key"))
				}
				if r.Header.Get("Authorization") != "Bearer dsa_test_secret_value" {
					t.Error("lost scoped credential")
				}
				raw, _ := io.ReadAll(r.Body)
				if len(test.args.Input) > 0 {
					var got, want any
					json.Unmarshal(raw, &got)
					json.Unmarshal(test.args.Input, &want)
					if string(recoveryJSON(got)) != string(recoveryJSON(want)) {
						t.Errorf("rewrote reviewed input: %s != %s", raw, test.args.Input)
					}
				} else if len(raw) != 0 {
					t.Error("invented request body")
				}
				if test.args.Key != "" {
					w.Header().Set("Idempotency-Replayed", "true")
				}
				io.WriteString(w, test.body)
			})
			out := c.Call(context.Background(), test.name, test.args)
			if !out.OK || out.Continuation == nil || out.Continuation.Kind != test.kind || out.Continuation.ID != test.id || out.Continuation.OperationID != test.operation || out.Continuation.Key != test.args.Key || calls.Load() != 1 {
				t.Fatalf("lost actual continuation: %+v %+v", out, out.Continuation)
			}
			if test.args.Key != "" && !out.Replayed {
				t.Fatal("lost replay status")
			}
		})
	}
}
func TestRecoveryRejectsMissingOrChangedReviewWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, test := range []struct {
		name string
		args Arguments
	}{
		{"service_delete", Arguments{RunID: "run-1", Key: "stable-delete-key", Input: json.RawMessage(`{"confirmation":{"resourceId":"run-1","action":"delete","expectedVersion":"v"}}`)}},
		{"service_delete", Arguments{RunID: "run-1", Key: "stable-delete-key", Input: confirmation("other-run", "delete")}},
		{"backup_restore", Arguments{BackupID: "backup-1", DestinationID: "destination-1", Key: "stable-restore-key", Input: confirmation("backup-1", "delete")}},
		{"backup_restore", Arguments{BackupID: "backup-1", DestinationID: "../destination", Key: "stable-restore-key", Input: confirmation("backup-1", "restore")}},
		{"backup_delete", Arguments{BackupID: "backup-1", Input: confirmation("backup-1", "delete")}},
		{"service_reconcile", Arguments{RunID: "run-1", Key: "ignored-retry-key"}},
		{"retention_review", Arguments{ProjectID: "project-1", Input: json.RawMessage(`{"scope":"history","expectedPolicy":{}}`)}},
		{"retention_review", Arguments{ProjectID: "project-1", Input: json.RawMessage(`{"scope":"runtime","expectedPolicy":{"projectId":"project-1","logDays":7,"runDays":30,"keepRuns":10}}`)}},
		{"retention_apply", Arguments{ProjectID: "project-1", Input: recoveryJSON(RuntimeRetentionApplyInput{Scope: "runtime", ExpectedPolicy: &recoveryPolicy, RuntimeReviewID: "review-1", RuntimeReviewDigest: "exact-digest"})}},
		{"retention_apply", Arguments{ProjectID: "project-1", Key: "unsupported-key", Input: recoveryJSON(RuntimeRetentionApplyInput{Scope: "runtime", ExpectedPolicy: &recoveryPolicy, Confirm: "project-1", RuntimeReviewID: "review-1", RuntimeReviewDigest: "exact-digest"})}},
		{"backup_create", Arguments{Key: "stable-backup-key", Input: json.RawMessage(`{"sourceRunId":"run-1","checks":[{"query":"SELECT 1"}]}`)}},
		{"backup_create", Arguments{Key: "stable-backup-key", Input: json.RawMessage(`{"sourceRunId":"run-1","verificationIntervalHours":8761}`)}},
		{"backup_create", Arguments{Key: "stable-backup-key", Input: json.RawMessage(`{"sourceRunId":"run-1","force":true}`)}},
	} {
		out := c.Call(context.Background(), test.name, test.args)
		if out.ExitCode() != 2 {
			t.Errorf("accepted incomplete/foreign request %s: %+v", test.name, out)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("client sent an unreviewed mutation")
	}
}
func TestRecoveryNeverRetriesLostResponseOrInventsReceipt(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		h := w.(http.Hijacker)
		conn, _, err := h.Hijack()
		if err == nil {
			conn.Close()
		}
	})
	for _, test := range []struct {
		name string
		args Arguments
		id   string
	}{
		{"service_retry", Arguments{RunID: "run-1"}, "run-1"},
		{"backup_reconcile", Arguments{BackupID: "backup-1", OperationID: "restore-1"}, "restore-1"},
		{"retention_apply", Arguments{ProjectID: "project-1", Input: recoveryJSON(RuntimeRetentionApplyInput{Scope: "runtime", ExpectedPolicy: &recoveryPolicy, Confirm: "project-1", RuntimeReviewID: "review-1", RuntimeReviewDigest: "exact-digest"})}, "review-1"},
	} {
		before := calls.Load()
		out := c.Call(context.Background(), test.name, test.args)
		if out.OK || calls.Load() != before+1 || out.Continuation == nil || out.Continuation.Kind == "receipt" || out.Continuation.ID != test.id || !strings.Contains(out.Error.Title, "Inspect the original") {
			t.Fatalf("unsafe interruption: %+v %+v", out, out.Continuation)
		}
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"id":"unknown-id"}`) })
	out := c.Call(context.Background(), "backup_verify", Arguments{BackupID: "backup-1", Key: "stable-verify-key"})
	if out.OK || out.Continuation != nil || out.Error.Code != "invalid_response" {
		t.Fatal("invented receipt from bare ID", out)
	}
}
func TestRecoveryMCPHasTypedInputsAndNoImplicitApproval(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workload-backups/backup-1/operations/op-1/reconcile" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("changed reconciliation")
		}
		io.WriteString(w, `{"id":"op-1","backupId":"backup-1","state":"unresolved"}`)
	})
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer receive.Close()
	done := make(chan error, 1)
	go func() { done <- c.ServeMCP(context.Background(), input, output); output.Close() }()
	enc := json.NewEncoder(send)
	scan := bufio.NewScanner(receive)
	scan.Buffer(make([]byte, 4096), MaxResponseBytes)
	read := func() map[string]any {
		t.Helper()
		if !scan.Scan() {
			t.Fatal("missing MCP reply", scan.Err())
		}
		var v map[string]any
		json.Unmarshal(scan.Bytes(), &v)
		return v
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}})
	read()
	enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	listed := read()["result"].(map[string]any)["tools"].([]any)
	found := map[string]map[string]any{}
	for _, raw := range listed {
		v := raw.(map[string]any)
		found[v["name"].(string)] = v
	}
	for _, op := range recoveryOperations {
		tool, ok := found[op.Name]
		if !ok {
			t.Fatal("missing recovery tool", op.Name)
		}
		hints := tool["annotations"].(map[string]any)
		if op.Destructive && hints["destructiveHint"] != true || op.NonIdempotent && hints["idempotentHint"] != false || op.CreatesReview && hints["readOnlyHint"] != false {
			t.Fatal("unsafe MCP hints", op.Name, hints)
		}
	}
	schema := found["backup_restore"]["inputSchema"].(map[string]any)["properties"].(map[string]any)["input"].(map[string]any)
	confirmation := schema["properties"].(map[string]any)["confirmation"].(map[string]any)
	if confirmation["additionalProperties"] != false || len(confirmation["required"].([]any)) != 4 || confirmation["properties"].(map[string]any)["action"].(map[string]any)["const"] != "restore" {
		t.Fatal("weak confirmation schema")
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "backup_reconcile", "arguments": map[string]any{"backupId": "backup-1", "operationId": "op-1"}}})
	result := read()["result"].(map[string]any)["structuredContent"].(map[string]any)
	continuation := result["continuation"].(map[string]any)
	if continuation["kind"] != "workload_backup_operation" || continuation["id"] != "op-1" || result["data"].(map[string]any)["state"] != "unresolved" {
		t.Fatal("MCP changed operation verdict", result)
	}
	send.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryReadOnlyDiscoveryAndReviews(t *testing.T) {
	tests := []struct {
		name, path string
		args       Arguments
	}{
		{"service_delete_review", "/service-provision-runs/run-1/resource/delete-preview", Arguments{RunID: "run-1"}},
		{"retention_policy", "/projects/project-1/retention", Arguments{ProjectID: "project-1"}},
		{"backups_list", "/workload-backups?projectId=project-1", Arguments{ProjectID: "project-1"}},
		{"backup_operations", "/workload-backups/backup-1/operations", Arguments{BackupID: "backup-1"}},
		{"backup_delete_review", "/workload-backups/backup-1/delete-preview", Arguments{BackupID: "backup-1"}},
		{"backup_restore_review", "/workload-backups/backup-1/restore/destination-1/preview", Arguments{BackupID: "backup-1", DestinationID: "destination-1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				op, _ := Find(test.name)
				if r.Method != op.Method || r.URL.RequestURI() != "/api/v1"+test.path || r.Header.Get("Idempotency-Key") != "" {
					t.Error("changed read/review scope")
				}
				io.WriteString(w, `{"blockedReason":"Active operation must be reconciled"}`)
			})
			out := c.Call(context.Background(), test.name, test.args)
			if !out.OK || calls != 1 || out.Continuation != nil || !strings.Contains(string(out.Data), "Active operation") {
				t.Fatal("review accepted action or hid blocker", out)
			}
		})
	}
}
func TestRecoveryPreservesForbiddenAndStaleReviewErrors(t *testing.T) {
	for _, status := range []int{403, 409, 422} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.WriteHeader(status)
				io.WriteString(w, `{"title":"Inspect the original resource","detail":"Current permission, ownership or review does not allow the action","review":{"blockedReason":"Retained data"}}`)
			})
			out := c.Call(context.Background(), "backup_restore", Arguments{BackupID: "backup-1", DestinationID: "destination-1", Key: "original-restore-key", Input: confirmation("backup-1", "restore")})
			if out.OK || out.Status != status || calls != 1 || !strings.Contains(string(out.Error.Evidence), "Retained data") || out.Continuation == nil || out.Continuation.Key != "original-restore-key" {
				t.Fatal("error prompted extra mutation or lost evidence", out)
			}
		})
	}
}

package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReviewedBackupRetirementClientAndMCPContracts(t *testing.T) {
	for _, operation := range []struct{ name, action, path string }{{"backup_retire_local", "retire-local", "retire-local"}, {"backup_delete_offsite", "delete-offsite", "delete-offsite"}} {
		t.Run(operation.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/v1/workload-backups/backup-1/"+operation.path || r.Header.Get("Idempotency-Key") != "stable-retirement-key" {
					t.Error("wrong reviewed operation or retry key")
				}
				var body RecoveryConfirmation
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Confirmation.Action != operation.action {
					t.Error("confirmation action was changed")
				}
				w.Header().Set("Location", "/api/v1/mutation-receipts/receipt-1")
				w.WriteHeader(202)
				io.WriteString(w, `{"id":"receipt-1","operationId":"original-op","operationKind":"workload_backup","resourceId":"backup-1"}`)
			}))
			defer server.Close()
			client, err := New(server.URL, "token", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			bad := Arguments{BackupID: "backup-1", Key: "stable-retirement-key", Input: confirmation("backup-1", "delete")}
			if result := client.Call(context.Background(), operation.name, bad); result.OK || calls != 0 {
				t.Fatal("mismatched confirmation reached server")
			}
			args := bad
			args.Input = confirmation("backup-1", operation.action)
			result := client.Call(context.Background(), operation.name, args)
			if !result.OK || result.Continuation == nil || result.Continuation.ID != "receipt-1" || result.Continuation.OperationID != "original-op" {
				t.Fatal("original receipt lost", result)
			}
			op, _ := Find(operation.name)
			tool := toolDescription(op)
			properties := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
			action := properties["input"].(map[string]any)["properties"].(map[string]any)["confirmation"].(map[string]any)["properties"].(map[string]any)["action"].(map[string]any)["const"]
			if action != operation.action {
				t.Fatal("MCP confirmation permits different action", action)
			}
		})
	}
}

package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestOffsiteDiscoveryExportAndVerificationKeepExplicitSelection(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workload-backup-stores":
			if r.URL.Query().Get("projectId") != "project-1" {
				t.Error("lost project filter")
			}
			io.WriteString(w, `[{"id":"store-1","projectId":"project-1"}]`)
		case "/api/v1/workload-backup-stores/store-1":
			io.WriteString(w, `{"id":"store-1","projectId":"project-1"}`)
		case "/api/v1/workload-backups/backup-1/export":
			var input WorkloadBackupExportInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.StoreID != "store-1" || r.Header.Get("Idempotency-Key") != "export-once" {
				t.Error("changed approved store or retry identity")
			}
			w.Header().Set("Location", "/api/v1/mutation-receipts/export-receipt")
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"export-receipt","operationId":"export-original","operationKind":"workload_backup","resourceId":"backup-1"}`)
		case "/api/v1/workload-backups/backup-1/verify":
			var input WorkloadBackupVerificationInput
			if json.NewDecoder(r.Body).Decode(&input) != nil || input.DestinationRunID != "fresh-target-service" || r.Header.Get("Idempotency-Key") != "verify-fresh-target" {
				t.Error("changed isolated target selection")
			}
			w.Header().Set("Location", "/api/v1/mutation-receipts/verify-receipt")
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"verify-receipt","operationId":"verify-original","operationKind":"workload_backup","resourceId":"backup-1"}`)
		default:
			t.Error("unexpected offsite route", r.URL.Path)
		}
	})
	for _, tc := range []struct {
		name string
		args Arguments
		id   string
	}{
		{"backup_stores_list", Arguments{ProjectID: "project-1"}, ""},
		{"backup_store_get", Arguments{StoreID: "store-1"}, ""},
		{"backup_export", Arguments{BackupID: "backup-1", Key: "export-once", Input: json.RawMessage(`{"storeId":"store-1"}`)}, "export-original"},
		{"backup_verify", Arguments{BackupID: "backup-1", Key: "verify-fresh-target", Input: json.RawMessage(`{"destinationRunId":"fresh-target-service"}`)}, "verify-original"},
	} {
		r := c.Call(context.Background(), tc.name, tc.args)
		if !r.OK {
			t.Fatal(tc.name, r)
		}
		if tc.id != "" && (r.Continuation == nil || r.Continuation.Kind != "receipt" || r.Continuation.OperationID != tc.id || r.Continuation.ResourceID != "backup-1") {
			t.Fatal("lost actual offsite operation identity", r)
		}
	}
}

func TestOffsiteMutationsRejectArbitraryEndpointsAndMissingSelection(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, tc := range []struct{ name, input string }{
		{"backup_export", `{}`},
		{"backup_export", `{"storeId":"store-1","url":"https://foreign.example/secret"}`},
		{"backup_verify", `{"destinationRunId":""}`},
		{"backup_verify", `{"destinationRunId":"service-1","approve":true}`},
	} {
		r := c.Call(context.Background(), tc.name, Arguments{BackupID: "backup-1", Key: "stable-offsite-key", Input: json.RawMessage(tc.input)})
		if r.OK || r.ExitCode() != 2 {
			t.Fatal("accepted unsafe offsite request", r)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid offsite selection reached server")
	}
}

func TestOffsiteLostReplyKeepsBackupAndRetryKey(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
	})
	r := c.Call(context.Background(), "backup_export", Arguments{BackupID: "backup-1", Key: "export-once", Input: json.RawMessage(`{"storeId":"store-1"}`)})
	if r.OK || calls.Load() != 1 || r.Continuation == nil || r.Continuation.ResourceID != "backup-1" || r.Continuation.Key != "export-once" {
		t.Fatal("lost export identity or replayed mutation", r)
	}
}

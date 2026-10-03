package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/automationclient"
)

func TestCLIJSONAndExplicitConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	os.WriteFile(path, []byte("dsa_cli_fixture"), 0600)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer dsa_cli_fixture" {
			t.Error("lost credential")
		}
		io.WriteString(w, `[{"id":"p-1"}]`)
	}))
	defer server.Close()
	var out, errout bytes.Buffer
	prefix := []string{"--url", server.URL, "--token-file", path}
	code := run(context.Background(), append(prefix, "projects", "list"), strings.NewReader(""), &out, &errout)
	var result automationclient.Result
	if json.Unmarshal(out.Bytes(), &result) != nil || code != 0 || !result.OK || requests != 1 {
		t.Fatal("CLI did not return JSON", code, out.String())
	}
	out.Reset()
	code = run(context.Background(), append(prefix, "server", "delete", "--server", "server-1", "--key", "stable-key", "--input", "-"), strings.NewReader(`{"digest":"x"}`), &out, &errout)
	if code != 2 || requests != 1 || strings.Contains(out.String(), "dsa_cli_fixture") {
		t.Fatal("CLI invented confirmation", code, out.String())
	}
}

func TestCLIManagedServerReadinessCommands(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credential, []byte("dsa_cli_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/infrastructure/servers/server-1" {
			t.Error("readiness command changed the server", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"id":"server-1","projectId":"project-1","allocationState":"allocated","enrollmentState":"enrolled","runtimeState":"verified-isolated","sourceSnapshotId":"snapshot-1","waitState":"verified-isolated","deployable":false,"latestOperation":{"id":"original-operation","serverId":"server-1"}}`)
	}))
	defer server.Close()
	for _, command := range []string{"get", "wait"} {
		var out, diagnostics bytes.Buffer
		args := []string{"--url", server.URL, "--token-file", credential, "server", command, "--server", "server-1"}
		if command == "wait" {
			args = append(args, "--timeout", "1")
		}
		code := run(context.Background(), args, strings.NewReader(""), &out, &diagnostics)
		var result automationclient.Result
		if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || !result.OK || result.Continuation == nil || result.Continuation.ID != "server-1" || result.Continuation.OperationID != "original-operation" || !strings.Contains(string(result.Data), `"deployable":false`) {
			t.Fatal("CLI lost isolated clone evidence", command, code, out.String(), diagnostics.String())
		}
	}
}

func TestMCPConfigurationFailureKeepsStdoutProtocolOnly(t *testing.T) {
	var out, errout bytes.Buffer
	code := run(context.Background(), []string{"--url", "https://example.com", "--token-file", filepath.Join(t.TempDir(), "missing"), "mcp"}, strings.NewReader(""), &out, &errout)
	if code != 2 || out.Len() != 0 || errout.Len() == 0 {
		t.Fatal("MCP config wrote non-protocol output")
	}
}

func TestCLIEnvironmentCleanupReviewUsesSelectedEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("dsa_cli_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/temporary-environments/environment-1/cleanup-review" {
			t.Error("incorrect cleanup review", r.Method, r.URL.Path)
		}
		io.WriteString(w, `{"environmentId":"environment-1","revision":7,"digest":"review-digest"}`)
	}))
	defer server.Close()
	var out, errout bytes.Buffer
	code := run(context.Background(), []string{"--url", server.URL, "--token-file", path, "environment", "cleanup", "review", "--environment", "environment-1"}, strings.NewReader(""), &out, &errout)
	var result automationclient.Result
	if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || !result.OK {
		t.Fatal("cleanup review command failed", code, out.String(), errout.String())
	}
}

func TestCLIRecoveryReviewAndOperationCommands(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credential, []byte("dsa_cli_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/api/v1/service-provision-runs/run-1/resource/delete-preview":
			io.WriteString(w, `{"resourceId":"run-1","version":"v1","name":"database","action":"delete"}`)
		case "/api/v1/workload-backups/backup-1/restore/destination-1/preview":
			io.WriteString(w, `{"resourceId":"backup-1","version":"v2","name":"destination","action":"restore"}`)
		case "/api/v1/workload-backup-operations/op-1":
			io.WriteString(w, `{"id":"op-1","backupId":"backup-1","state":"unresolved"}`)
		default:
			t.Errorf("unexpected command path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	prefix := []string{"--url", server.URL, "--token-file", credential}
	for _, args := range [][]string{
		{"service", "delete", "review", "--run", "run-1"},
		{"backup", "restore", "review", "--backup", "backup-1", "--destination", "destination-1"},
		{"backup", "operation", "get", "--operation", "op-1"},
	} {
		var out, diagnostics bytes.Buffer
		if code := run(context.Background(), append(prefix, args...), strings.NewReader(""), &out, &diagnostics); code != 0 {
			t.Fatalf("command %v failed: %s %s", args, out.String(), diagnostics.String())
		}
		var result automationclient.Result
		if json.Unmarshal(out.Bytes(), &result) != nil || !result.OK {
			t.Fatal("not structured JSON")
		}
		if args[1] == "operation" && (result.Continuation == nil || result.Continuation.Kind != "workload_backup_operation" || result.Continuation.ID != "op-1") {
			t.Fatal("wrong operation continuation", out.String())
		}
	}
	var out, diagnostics bytes.Buffer
	args := append(prefix, "backup", "restore", "--backup", "backup-1", "--destination", "destination-1", "--key", "stable-restore-key", "--input", "-")
	code := run(context.Background(), args, strings.NewReader(`{"confirmation":{"resourceId":"backup-1","action":"restore","expectedVersion":"v2"}}`), &out, &diagnostics)
	if code != 2 || calls != 3 {
		t.Fatal("CLI supplied missing restore confirmation", code, calls, out.String())
	}
}

func TestCLIApplicationConfigurationAndInfrastructureRecovery(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credential, []byte("dsa_cli_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dsa_cli_fixture" {
			t.Error("lost credential")
		}
		switch r.URL.Path {
		case "/api/v1/apps/app-1/helm-values":
			mutations++
			if r.Method != "PUT" || r.Header.Get("Idempotency-Key") != "" {
				t.Error("configuration request manufactured a receipt contract")
			}
			io.WriteString(w, `{"overrides":{}}`)
		case "/api/v1/apps":
			mutations++
			if r.Header.Get("Idempotency-Key") != "create-app-once" {
				t.Error("lost app creation key")
			}
			w.Header().Set("Location", "/api/v1/mutation-receipts/receipt-1")
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"receipt-1","operationId":"app-1","resourceId":"app-1","operationKind":"application"}`)
		case "/api/v1/infrastructure/servers/server-1/operations":
			io.WriteString(w, `[{"id":"operation-1","serverId":"server-1"}]`)
		case "/api/v1/infrastructure/operations/operation-1/retry":
			mutations++
			w.WriteHeader(204)
		default:
			t.Error("wrong command route", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	prefix := []string{"--url", server.URL, "--token-file", credential}
	for _, tc := range []struct {
		args  []string
		input string
		kind  string
	}{
		{[]string{"app", "helm", "values", "set", "--app", "app-1", "--input", "-"}, `{"overrides":{}}`, "application"},
		{[]string{"app", "create", "--key", "create-app-once", "--input", "-"}, `{"projectId":"project-1","serverId":"server-1","name":"web","composeContent":"services: {}"}`, "receipt"},
		{[]string{"server", "retry", "--server", "server-1", "--operation", "operation-1"}, "", "managed_server"},
	} {
		var out, diagnostics bytes.Buffer
		code := run(context.Background(), append(prefix, tc.args...), strings.NewReader(tc.input), &out, &diagnostics)
		var result automationclient.Result
		if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || !result.OK || result.Continuation == nil || result.Continuation.Kind != tc.kind {
			t.Fatal("lost command continuation", tc.args, code, out.String(), diagnostics.String())
		}
	}
	if mutations != 3 {
		t.Fatal("unexpected mutations", mutations)
	}
}

func TestCLIBackupPolicyPauseRequiresExplicitEnabled(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credential, []byte("dsa_cli_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "PUT" || r.URL.Path != "/api/v1/workload-backup-policies/policy-1" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("wrong policy mutation")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["enabled"] != false || body["confirmName"] != "daily-pg" {
			t.Error("pause was not explicit")
		}
		io.WriteString(w, `{"id":"policy-1","revision":2,"enabled":false}`)
	}))
	defer server.Close()
	args := []string{"--url", server.URL, "--token-file", credential, "backup", "policy", "set", "--policy", "policy-1", "--input", "-"}
	var out, diagnostics bytes.Buffer
	if code := run(context.Background(), args, strings.NewReader(`{"revision":1,"confirmName":"daily-pg"}`), &out, &diagnostics); code != 2 || requests != 0 {
		t.Fatal("CLI silently paused policy", code, requests)
	}
	out.Reset()
	if code := run(context.Background(), args, strings.NewReader(`{"revision":1,"enabled":false,"confirmName":"daily-pg"}`), &out, &diagnostics); code != 0 || requests != 1 {
		t.Fatal("explicit pause failed", code, out.String())
	}
}

func TestCLIOffsiteSelectionAndContinuation(t *testing.T) {
	credential := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credential, []byte("dsa_cli_fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/workload-backup-stores/store-1":
			io.WriteString(w, `{"id":"store-1","projectId":"project-1"}`)
		case "/api/v1/workload-backups/backup-1/export", "/api/v1/workload-backups/backup-1/verify":
			var input map[string]string
			if json.NewDecoder(r.Body).Decode(&input) != nil {
				t.Error("missing explicit selection")
			}
			if strings.HasSuffix(r.URL.Path, "/export") && input["storeId"] != "store-1" {
				t.Error("changed store selection")
			}
			if strings.HasSuffix(r.URL.Path, "/verify") && input["destinationRunId"] != "fresh-service" {
				t.Error("changed verification target")
			}
			if r.Header.Get("Idempotency-Key") != "offsite-once" {
				t.Error("lost retry identity")
			}
			w.Header().Set("Location", "/api/v1/mutation-receipts/receipt-1")
			w.WriteHeader(202)
			io.WriteString(w, `{"id":"receipt-1","operationId":"original-operation","operationKind":"workload_backup","resourceId":"backup-1"}`)
		default:
			t.Error("unexpected offsite CLI route", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	prefix := []string{"--url", server.URL, "--token-file", credential}
	for _, tc := range []struct {
		args  []string
		input string
	}{
		{[]string{"backup", "store", "get", "--store", "store-1"}, ""},
		{[]string{"backup", "export", "--backup", "backup-1", "--key", "offsite-once", "--input", "-"}, `{"storeId":"store-1"}`},
		{[]string{"backup", "verify", "--backup", "backup-1", "--key", "offsite-once", "--input", "-"}, `{"destinationRunId":"fresh-service"}`},
	} {
		var out, diagnostics bytes.Buffer
		code := run(context.Background(), append(prefix, tc.args...), strings.NewReader(tc.input), &out, &diagnostics)
		var result automationclient.Result
		if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil || !result.OK {
			t.Fatal("offsite CLI failed", tc.args, out.String(), diagnostics.String())
		}
		if tc.input != "" && (result.Continuation == nil || result.Continuation.OperationID != "original-operation" || result.Continuation.ResourceID != "backup-1") {
			t.Fatal("lost accepted offsite identity", out.String())
		}
	}
}

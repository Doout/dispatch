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

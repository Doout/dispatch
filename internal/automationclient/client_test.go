package automationclient

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, e := New(s.URL, "dsa_test_secret_value", time.Second)
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestCredentialTransportAndBoundaries(t *testing.T) {
	for _, endpoint := range []string{"http://controller.example", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=x", "https://example.com#fragment"} {
		if _, e := New(endpoint, "token", time.Second); e == nil {
			t.Fatal("accepted unsafe origin", endpoint)
		}
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "token")
	if e := os.WriteFile(file, []byte("dsa_test_secret_value\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := ReadToken(file); e == nil {
		t.Fatal("accepted public credential file")
	}
	os.Chmod(file, 0600)
	if token, e := ReadToken(file); e != nil || token != "dsa_test_secret_value" {
		t.Fatal("private credential", e)
	}
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dsa_test_secret_value" {
			t.Error("missing authentication")
		}
		http.Redirect(w, r, destination.URL, 302)
	})
	if r := c.Call(context.Background(), "projects_list", Arguments{}); r.OK || leaked.Load() {
		t.Fatal("followed credential redirect")
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		io.WriteString(w, `{"title":"dsa_test_secret_value denied","detail":"Credential dsa_test_secret_value","quota":{"usage":2}}`)
	})
	r := c.Call(context.Background(), "projects_list", Arguments{})
	b, _ := json.Marshal(r)
	if r.ExitCode() != 3 || strings.Contains(string(b), "dsa_test_secret_value") || !strings.Contains(string(b), "quota") {
		t.Fatal("unsafe structured failure", string(b))
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"data":"`+strings.Repeat("x", MaxResponseBytes)+`"}`)
	})
	if r := c.Call(context.Background(), "projects_list", Arguments{}); r.Error == nil || r.Error.Code != "response_limit" {
		t.Fatal("unbounded response")
	}
}
func TestTypedMutationsPreserveKeyAndNeverInventConfirmation(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/infrastructure/servers/server-1/delete" || r.Header.Get("Idempotency-Key") != "stable-delete-key" {
			t.Error("changed identity")
		}
		var body InfrastructureAcceptance
		json.NewDecoder(r.Body).Decode(&body)
		if body.ConfirmName != "production" || body.Digest != "review-digest" {
			t.Error("changed confirmation")
		}
		w.Header().Set("Location", "/api/v1/mutation-receipts/original")
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(202)
		io.WriteString(w, `{"id":"original","operationId":"op-1","state":"accepted"}`)
	})
	for _, a := range []Arguments{{ServerID: "../bad", Key: "stable-delete-key", Input: json.RawMessage(`{}`)}, {ServerID: "server-1", Key: "stable-delete-key", Input: json.RawMessage(`{"digest":"review-digest"}`)}, {ServerID: "server-1", Input: json.RawMessage(`{"digest":"review-digest","confirmName":"production"}`)}, {ServerID: "server-1", Key: "stable-delete-key", Input: json.RawMessage(`{"digest":"review-digest","confirmName":"production","approve":true}`)}} {
		if r := c.Call(context.Background(), "server_delete", a); r.OK || r.ExitCode() != 2 {
			t.Fatal("invalid mutation reached server", r)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("manufactured confirmation")
	}
	args := Arguments{ServerID: "server-1", Key: "stable-delete-key", Input: json.RawMessage(`{"digest":"review-digest","confirmName":"production"}`)}
	for range 2 {
		r := c.Call(context.Background(), "server_delete", args)
		if !r.OK || !r.Replayed || r.Location != "/api/v1/mutation-receipts/original" {
			t.Fatal(r)
		}
	}
}
func TestWaitCancellationPreservesIdentityAndDoesNotCancelWork(t *testing.T) {
	var mutations atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			mutations.Add(1)
		}
		io.WriteString(w, `{"id":"op-1","state":"running"}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	r := c.Call(ctx, "deployment_wait", Arguments{DeploymentID: "op-1", TimeoutSeconds: 1})
	if r.ExitCode() != 4 || r.Continuation == nil || r.Continuation.ID != "op-1" || mutations.Load() != 0 {
		t.Fatal("lost continuation or cancelled server work", r)
	}
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"op-1","state":"unresolved","recoveryActions":["inspect_runtime"]}`)
	})
	r = c.Call(context.Background(), "receipt_wait", Arguments{ReceiptID: "receipt-1"})
	if r.ExitCode() != 5 || r.Error.Code != "outcome_unknown" || !strings.Contains(string(r.Data), "inspect_runtime") {
		t.Fatal("uncertain outcome was hidden", r)
	}
}
func TestMCPToolsLifecycleCancellationAndNoApprovalTool(t *testing.T) {
	requested := make(chan struct{}, 1)
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		requested <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	input, send := io.Pipe()
	receive, output := io.Pipe()
	defer receive.Close()
	done := make(chan error, 1)
	go func() { done <- c.ServeMCP(context.Background(), input, output); output.Close() }()
	enc := json.NewEncoder(send)
	scan := bufio.NewScanner(receive)
	read := func() map[string]any {
		t.Helper()
		if !scan.Scan() {
			t.Fatal("missing MCP response", scan.Err())
		}
		var v map[string]any
		if json.Unmarshal(scan.Bytes(), &v) != nil {
			t.Fatal("non-protocol output")
		}
		return v
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-11-25"}})
	if read()["result"] == nil {
		t.Fatal("initialization failed")
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	list := read()
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), `"name":"approve"`) || !strings.Contains(string(raw), `"name":"server_delete_review"`) {
		t.Fatal("incorrect tools")
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "id": "wait-1", "method": "tools/call", "params": map[string]any{"name": "deployment_wait", "arguments": map[string]any{"deploymentId": "op-1"}}})
	select {
	case <-requested:
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": "wait-1"}})
	response := read()
	raw, _ = json.Marshal(response)
	if !strings.Contains(string(raw), `"cancelled"`) || !strings.Contains(string(raw), `"continuation"`) {
		t.Fatal("cancellation lost receipt", string(raw))
	}
	send.Close()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestMCPShutdownDoesNotWaitForAnotherInputLine(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	input, send := io.Pipe()
	defer send.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.ServeMCP(ctx, input, io.Discard) }()
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP shutdown blocked reading stdin")
	}
}
func TestJSONEvidencePreservesIntegerCursors(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[{"id":9007199254740993,"message":"log"}]`)
	})
	r := c.Call(context.Background(), "deployment_logs", Arguments{DeploymentID: "deployment-1", Limit: 1})
	if !r.OK || !strings.Contains(string(r.Data), "9007199254740993") {
		t.Fatal("log continuation cursor lost precision", string(r.Data))
	}
}

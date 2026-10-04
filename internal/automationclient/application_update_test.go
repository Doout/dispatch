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

func TestApplicationUpdateSendsOnlyExplicitChangesAndCurrentSpec(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "PATCH" || r.URL.Path != "/api/v1/apps/original-app" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("changed application request", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var got map[string]any
		if json.Unmarshal(raw, &got) != nil || len(got) != 3 || got["expectedSpecDigest"] != digest || got["domain"] != "" || got["containerPort"] != float64(0) {
			t.Error("invented fields or discarded explicit clears", string(raw))
		}
		io.WriteString(w, `{"id":"original-app","specDigest":"sha256:`+strings.Repeat("b", 64)+`","domain":"","containerPort":0}`)
	})
	args := Arguments{AppID: "original-app", Input: json.RawMessage(`{"expectedSpecDigest":"` + digest + `","domain":"","containerPort":0}`)}
	out := c.Call(context.Background(), "app_update", args)
	if !out.OK || out.Continuation == nil || out.Continuation.Kind != "application" || out.Continuation.ID != args.AppID || requests.Load() != 1 {
		t.Fatal("update lost original application continuation", out, requests.Load())
	}
}

func TestApplicationUpdateRejectsCredentialAndIdentityFieldsBeforeTransport(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	for _, body := range []string{
		`{"branch":"release"}`,
		`{"expectedSpecDigest":"` + digest + `"}`,
		`{"expectedSpecDigest":"` + digest + `","branch":null}`,
		`{"expectedSpecDigest":"` + digest + `","sourceCredentialId":"global-key"}`,
		`{"expectedSpecDigest":"` + digest + `","sourceAuthType":"github_token"}`,
		`{"expectedSpecDigest":"` + digest + `","serverId":"new-target"}`,
		`{"expectedSpecDigest":"` + digest + `","name":"new-name"}`,
		`{"expectedSpecDigest":"` + digest + `","containerPort":65536}`,
		`{"expectedSpecDigest":"` + digest + `","buildType":"shell"}`,
	} {
		out := c.Call(context.Background(), "app_update", Arguments{AppID: "original-app", Input: json.RawMessage(body)})
		if out.OK || out.Error == nil || out.Error.Code != "invalid_input" {
			t.Fatal("invalid update reached transport", body, out)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid update sent a request", requests.Load())
	}
	schema := applicationUpdateSchema("ApplicationUpdateInput")
	props := schema["properties"].(map[string]any)
	if props["sourceCredentialId"] != nil || props["sourceAuthType"] != nil || props["serverId"] != nil || schema["additionalProperties"] != false {
		t.Fatal("MCP schema allows owner-only or identity updates", schema)
	}
}

func TestApplicationUpdateResponseLossPreservesOriginalResourceWithoutRetry(t *testing.T) {
	args := Arguments{AppID: "original-app"}
	out := applicationUpdateResult(appUpdateOperations[0], args, Failure("timeout", "Timed out"))
	if out.Continuation == nil || out.Continuation.ID != args.AppID || out.Error == nil || !strings.Contains(out.Error.Title, "Inspect the same application") {
		t.Fatal("response loss suggested another mutation", out)
	}
	for _, response := range []string{`{}`, `{"id":"another-app","specDigest":"sha256:` + strings.Repeat("a", 64) + `"}`, `{"id":"original-app","specDigest":"missing"}`} {
		out := applicationUpdateResult(appUpdateOperations[0], args, Result{Version: "dispatch.client/v1", OK: true, Data: json.RawMessage(response)})
		if out.OK || out.Error == nil || out.Error.Code != "invalid_response" || out.Continuation.ID != args.AppID {
			t.Fatal("accepted mismatched resource response", out)
		}
	}
}

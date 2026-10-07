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

const bootstrapFixture = `{"id":"bootstrap-1","serverId":"server-1","projectId":"project-1","digest":"original-digest","state":"accepted"}`

func TestBootstrapClientPreservesOriginalInstallationAndFilters(t *testing.T) {
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/api/v1/infrastructure/bootstrap":
			if r.Method != "GET" || r.URL.Query().Get("serverId") != "server-1" || r.URL.Query().Get("projectId") != "project-1" {
				t.Error("lost installation inventory scope")
				return
			}
			io.WriteString(w, "["+bootstrapFixture+"]")
		case "/api/v1/infrastructure/bootstrap/bootstrap-1":
			if r.Method != "GET" {
				t.Error("inspection mutated the installation")
				return
			}
			io.WriteString(w, bootstrapFixture)
		case "/api/v1/infrastructure/bootstrap/bootstrap-1/retry":
			var input BootstrapRetryInput
			if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&input) != nil || input.Digest != "original-digest" || r.Header.Get("Idempotency-Key") != "" {
				t.Error("changed the original installation retry")
				return
			}
			w.WriteHeader(202)
			io.WriteString(w, bootstrapFixture)
		default:
			t.Error("unexpected installation path", r.URL.Path)
		}
	})
	list := c.Call(context.Background(), "bootstraps_list", Arguments{ServerID: "server-1", ProjectID: "project-1"})
	if !list.OK {
		t.Fatal(list)
	}
	for _, name := range []string{"bootstrap_get", "bootstrap_retry"} {
		args := Arguments{BootstrapID: "bootstrap-1"}
		if name == "bootstrap_retry" {
			args.Input = json.RawMessage(`{"digest":"original-digest"}`)
		}
		out := c.Call(context.Background(), name, args)
		if !out.OK || out.Continuation == nil || out.Continuation.Kind != "target_bootstrap" || out.Continuation.ID != "bootstrap-1" || out.Continuation.ResourceID != "server-1" || out.Continuation.ProjectID != "project-1" {
			t.Fatal("lost original installation continuation", name, out)
		}
	}
	if got := requests.Load(); got != 3 {
		t.Fatal("repeated installation request", got)
	}
}

func TestBootstrapClientRejectsChangedReceiptsAndNeverRetriesResponseLoss(t *testing.T) {
	for _, body := range []string{strings.ReplaceAll(bootstrapFixture, "bootstrap-1", "another-bootstrap"), strings.ReplaceAll(bootstrapFixture, "original-digest", "another-digest"), `null`, `{}`} {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		out := c.Call(context.Background(), "bootstrap_retry", Arguments{BootstrapID: "bootstrap-1", Input: json.RawMessage(`{"digest":"original-digest"}`)})
		if out.OK || out.Error == nil || out.Error.Code != "invalid_response" || out.Continuation.ID != "bootstrap-1" {
			t.Fatal("accepted a changed installation", body, out)
		}
	}
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	out := c.Call(context.Background(), "bootstrap_retry", Arguments{BootstrapID: "bootstrap-1", Input: json.RawMessage(`{"digest":"original-digest"}`)})
	if got := requests.Load(); out.OK || got != 1 || out.Continuation == nil || out.Continuation.ID != "bootstrap-1" || !strings.Contains(out.Error.Title, "No automatic retry") {
		t.Fatal("lost or replayed the original installation", got, out)
	}
}

func TestBootstrapClientRequiresSavedDigestAndExposesOnlyRecoveryTools(t *testing.T) {
	var requests atomic.Int32
	c := testClient(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	for _, input := range []string{`null`, `{}`, `{"digest":""}`, `{"digest":"original-digest","confirmName":"invented"}`} {
		out := c.Call(context.Background(), "bootstrap_retry", Arguments{BootstrapID: "bootstrap-1", Input: json.RawMessage(input)})
		if out.OK || out.ExitCode() != 2 || requests.Load() != 0 {
			t.Fatal("invalid retry reached the controller", input, out)
		}
	}
	if _, exists := Find("bootstrap_accept"); exists {
		t.Fatal("installation approval was exposed as an automation tool")
	}
	op, _ := Find("bootstrap_retry")
	description := toolDescription(op)
	annotations := description["annotations"].(map[string]any)
	if annotations["readOnlyHint"] != false || annotations["idempotentHint"] != false || annotations["destructiveHint"] != true {
		t.Fatal("incorrect installation retry hints", annotations)
	}
}

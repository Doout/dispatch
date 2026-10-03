package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestEnvironmentMutationKeepsReviewKeyAndContinuation(t *testing.T) {
	var requests atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/v1/temporary-environments/environment-1/destroy" || r.Header.Get("Idempotency-Key") != "cleanup-environment-1" {
			t.Error("changed cleanup identity", r.URL.Path)
		}
		var input TemporaryEnvironmentCleanup
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Revision != 7 || input.Digest != "review-digest" || input.ConfirmName != "agent-test" {
			t.Error("changed reviewed cleanup", input)
		}
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(202)
		io.WriteString(w, `{"id":"receipt-1","operationId":"cleanup-1","resourceId":"environment-1","state":"accepted"}`)
	})
	for _, input := range []string{
		`{"revision":7,"digest":"review-digest"}`,
		`{"revision":0,"digest":"review-digest","confirmName":"agent-test"}`,
		`{"revision":7,"digest":"review-digest","confirmName":"agent-test","force":true}`,
	} {
		r := c.Call(context.Background(), "environment_destroy", Arguments{EnvironmentID: "environment-1", Key: "cleanup-environment-1", Input: json.RawMessage(input)})
		if r.ExitCode() != 2 {
			t.Fatal("invalid cleanup accepted", r)
		}
	}
	if r := c.Call(context.Background(), "environment_create", Arguments{Key: "create-environment-1", Input: json.RawMessage(`{"digest":"review-digest","confirmName":"agent-test"}`)}); r.ExitCode() != 2 {
		t.Fatal("created without the reviewed identity", r)
	}
	if requests.Load() != 0 {
		t.Fatal("client supplied missing confirmation")
	}
	for range 2 {
		r := c.Call(context.Background(), "environment_destroy", Arguments{EnvironmentID: "environment-1", Key: "cleanup-environment-1", Input: json.RawMessage(`{"revision":7,"digest":"review-digest","confirmName":"agent-test"}`)})
		if !r.OK || !r.Replayed || r.Continuation == nil || r.Continuation.Kind != "receipt" || r.Continuation.ID != "receipt-1" || r.Continuation.Key != "cleanup-environment-1" {
			t.Fatal("lost original cleanup receipt", r)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("client made an extra mutation", requests.Load())
	}
}

func TestEnvironmentReviewPreservesExplicitBindingsAndGeneratedRouteEvidence(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		var input TemporaryEnvironmentInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.ServiceBindings) != 1 || input.ServiceBindings[0].ServiceRef != "approved-service" || input.ServiceBindings[0].Environment["DATABASE_URL"] != "connectionUrl" {
			t.Error("changed explicit service composition")
		}
		io.WriteString(w, `{"id":"review-1","serviceRevisions":{"approved-service":3},"route":{"hostname":"generated.preview.example"},"routing":{"enabled":true}}`)
	})
	input := TemporaryEnvironmentInput{ProjectID: "project-1", TemplateID: "template-1", ServerID: "server-1", Name: "agent-test", SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 3600, ServiceBindings: []core.ServiceBinding{{Alias: "db", ServiceRef: "approved-service", Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}}
	raw, _ := json.Marshal(input)
	r := c.Call(context.Background(), "environment_review", Arguments{Input: raw})
	if !r.OK || !strings.Contains(string(r.Data), "generated.preview.example") || !strings.Contains(string(r.Data), "serviceRevisions") {
		t.Fatal("lost reviewed route or binding evidence", r)
	}
	bad := input
	bad.ServiceBindings = []core.ServiceBinding{{Alias: "db", ServiceRef: "approved-service", Compose: map[string]map[string]string{"web": {"DATABASE_URL": "connectionUrl"}}}}
	raw, _ = json.Marshal(bad)
	if r := c.Call(context.Background(), "environment_review", Arguments{Input: raw}); r.OK || r.ExitCode() != 2 {
		t.Fatal("accepted unsupported Compose binding")
	}
	bad.ServiceBindings = make([]core.ServiceBinding, 33)
	raw, _ = json.Marshal(bad)
	if r := c.Call(context.Background(), "environment_review", Arguments{Input: raw}); r.OK || r.ExitCode() != 2 {
		t.Fatal("accepted unbounded environment services")
	}
	var fields map[string]any
	json.Unmarshal(recoveryJSON(input), &fields)
	fields["hostname"] = "production.example"
	raw, _ = json.Marshal(fields)
	if r := c.Call(context.Background(), "environment_review", Arguments{Input: raw}); r.OK || r.ExitCode() != 2 {
		t.Fatal("accepted caller-selected production hostname")
	}
	schema := inputSchema("TemporaryEnvironmentInput").(map[string]any)["properties"].(map[string]any)["serviceBindings"].(map[string]any)
	if schema["maxItems"] != 32 {
		t.Fatal("MCP services were not bounded")
	}
	props := schema["items"].(map[string]any)["properties"].(map[string]any)
	if props["compose"] != nil || props["helm"] != nil {
		t.Fatal("MCP advertised unsupported environment mappings")
	}
}

func TestEnvironmentReviewRequiresPinnedSourceAndFiniteLifetime(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var input TemporaryEnvironmentInput
		json.NewDecoder(r.Body).Decode(&input)
		if r.URL.Path != "/api/v1/temporary-environments/review" || input.LifetimeSeconds != 3600 || input.SourceSHA != strings.Repeat("a", 40) {
			t.Error("changed reviewed lifetime or source", input)
		}
		io.WriteString(w, `{"id":"review-1","state":"prepared"}`)
	})
	input := TemporaryEnvironmentInput{ProjectID: "p-1", TemplateID: "template-1", ServerID: "server-1", Name: "agent-test", SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 3600}
	for _, changed := range []TemporaryEnvironmentInput{
		{ProjectID: input.ProjectID, TemplateID: input.TemplateID, ServerID: input.ServerID, Name: input.Name, SourceSHA: "main", LifetimeSeconds: 3600},
		{ProjectID: input.ProjectID, TemplateID: input.TemplateID, ServerID: input.ServerID, Name: input.Name, SourceSHA: input.SourceSHA, LifetimeSeconds: 0},
	} {
		raw, _ := json.Marshal(changed)
		if r := c.Call(context.Background(), "environment_review", Arguments{Input: raw}); r.ExitCode() != 2 {
			t.Fatal("unpinned or unbounded environment reached API", r)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid review reached API")
	}
	raw, _ := json.Marshal(input)
	if r := c.Call(context.Background(), "environment_review", Arguments{Input: raw}); !r.OK || calls.Load() != 1 {
		t.Fatal(r)
	}
}

func TestEnvironmentExtensionDoesNotInventReceiptOrReplay(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/temporary-environments/environment-1/extend" || r.Header.Get("Idempotency-Key") != "" {
			t.Error("changed extension identity")
		}
		io.WriteString(w, `{"id":"environment-1","revision":8,"expiresAt":"2030-01-02T00:00:00Z"}`)
	})
	r := c.Call(context.Background(), "environment_extend", Arguments{EnvironmentID: "environment-1", Input: json.RawMessage(`{"revision":7,"expiresAt":"2030-01-02T00:00:00Z"}`)})
	if !r.OK || r.Continuation == nil || r.Continuation.Kind != "environment" || r.Continuation.ID != "environment-1" || calls.Load() != 1 {
		t.Fatal("extension was treated as a receipt", r)
	}
	for _, name := range []string{"environment_review", "environment_extend", "environment_destroy"} {
		op, _ := Find(name)
		hints := toolDescription(op)["annotations"].(map[string]any)
		if hints["readOnlyHint"] != false || name == "environment_extend" && hints["idempotentHint"] != false || name == "environment_destroy" && hints["destructiveHint"] != true {
			t.Fatal("incorrect mutation annotations", name, hints)
		}
	}
}

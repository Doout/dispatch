package automationclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestServiceProvisionKeepsTemplateInputAndReceiptIdentity(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/service-templates/template-1/runs" || r.Header.Get("Idempotency-Key") != "service-create-once" {
			t.Error("changed service request identity")
		}
		var input ServiceProvisionInput
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.Name != "review-database" || input.Inputs["database"] != "review" {
			t.Error("changed explicit template input")
		}
		w.Header().Set("Location", "/api/v1/mutation-receipts/service-receipt")
		w.Header().Set("Idempotency-Replayed", "true")
		w.WriteHeader(202)
		io.WriteString(w, `{"id":"service-receipt","operationId":"service-run","resourceId":"template-1","operationKind":"service_provision"}`)
	})
	args := Arguments{TemplateID: "template-1", Key: "service-create-once", Input: json.RawMessage(`{"name":"review-database","inputs":{"database":"review"}}`)}
	for range 2 {
		r := c.Call(context.Background(), "service_provision", args)
		if !r.OK || !r.Replayed || r.Continuation == nil || r.Continuation.Kind != "receipt" || r.Continuation.OperationID != "service-run" || r.Continuation.Key != args.Key {
			t.Fatal("lost original provision receipt", r)
		}
	}
}

func TestServiceProvisionRejectsImplicitInputsAndDataCopy(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, input := range []string{`{"name":"database"}`, `{"name":"database","inputs":null}`, `{"name":"database","inputs":{},"confirmDataCopy":"Copy parent rows into database"}`} {
		r := c.Call(context.Background(), "service_provision", Arguments{TemplateID: "template-1", Key: "service-create-once", Input: json.RawMessage(input)})
		if r.OK || r.ExitCode() != 2 {
			t.Fatal("accepted implicit or data-copy request", r)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid provision reached server")
	}
}

func TestServiceDiscoveryFiltersProjectAndRunKeepsOriginalID(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/service-provision-runs/run-1" {
			io.WriteString(w, `{"id":"run-1","state":"running"}`)
			return
		}
		io.WriteString(w, `[{"id":"allowed","projectId":"project-1"},{"id":"other","projectId":"project-2"}]`)
	})
	for _, op := range []string{"service_templates_list", "service_runs_list"} {
		r := c.Call(context.Background(), op, Arguments{ProjectID: "project-1"})
		if !r.OK || string(r.Data) != `[{"id":"allowed","projectId":"project-1"}]` {
			t.Fatal("service project filter lost", r)
		}
	}
	r := c.Call(context.Background(), "service_run_get", Arguments{RunID: "run-1"})
	if !r.OK || r.Continuation == nil || r.Continuation.Kind != "service_provision_run" || r.Continuation.ID != "run-1" {
		t.Fatal("lost service run ID", r)
	}
}

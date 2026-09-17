package api

import (
	"bufio"
	"context"
	"github.com/doout/dispatch/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkflowLogDelta(t *testing.T) {
	before := core.WorkflowJobResult{Log: "🚀 build\n"}
	after := core.WorkflowJobResult{ID: "job", JobName: "build", State: "running", Log: "🚀 build\nnext"}
	delta := workflowLogChange(before, after)
	if delta.Keep != 9 || delta.Text != "next" {
		t.Fatalf("wrong unicode delta: %+v", delta)
	}
	after.Log = "🚀 build\n[redacted]"
	delta = workflowLogChange(core.WorkflowJobResult{Log: "🚀 build\nsecret"}, after)
	if delta.Keep != 9 || delta.Text != "[redacted]" {
		t.Fatalf("wrong replacement: %+v", delta)
	}
}

func TestWorkflowLogStream(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	a := handler.(*API)
	ctx := context.Background()
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(a.store.CreateSecret(ctx, core.Secret{ID: "logs-key", Name: "logs key", CreatedAt: now, UpdatedAt: now}))
	must(a.store.CreateConfigSource(ctx, core.ConfigSource{CredentialSecretID: "logs-key", ID: "logs-source", ProjectID: projects[0].ID, Name: "logs", CreatedAt: now, UpdatedAt: now}))
	must(a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "logs-resource", ConfigSourceID: "logs-source", Name: "build", Kind: "Application", CreatedAt: now, UpdatedAt: now}))
	revision := core.WorkflowRevision{ID: "logs-revision", ResourceID: "logs-resource", State: "running", CreatedAt: now}
	must(a.store.CreateWorkflowRevision(ctx, revision))
	job := core.WorkflowJobResult{ID: "logs-job", ResourceID: "logs-resource", RevisionID: revision.ID, JobName: "build", State: "running", Log: "first", CreatedAt: now}
	must(a.store.CreateWorkflowJobResult(ctx, job))
	server := httptest.NewServer(handler)
	defer server.Close()
	requestCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(requestCtx, "GET", server.URL+"/api/v1/workflow/revisions/logs-revision/logs/watch", nil)
	unauthorized, err := http.DefaultClient.Do(req)
	must(err)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != 401 {
		t.Fatalf("unauthenticated status %d", unauthorized.StatusCode)
	}
	req.Header.Set("Authorization", "Bearer secret")
	response, err := http.DefaultClient.Do(req)
	must(err)
	defer response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("stream status %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	nextData := func() string {
		t.Helper()
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				return scanner.Text()
			}
		}
		t.Fatal("stream ended")
		return ""
	}
	nextData() // connected
	if data := nextData(); !strings.Contains(data, `"text":"first"`) {
		t.Fatalf("initial log: %s", data)
	}
	job.Log = "first second"
	must(a.store.UpdateWorkflowJobResult(ctx, job))
	if data := nextData(); !strings.Contains(data, `"keep":5`) || !strings.Contains(data, `"text":" second"`) {
		t.Fatalf("not a delta: %s", data)
	}
	job.State = "succeeded"
	must(a.store.UpdateWorkflowJobResult(ctx, job))
	revision.State = "succeeded"
	must(a.store.UpdateWorkflowRevision(ctx, revision))
	if data := nextData(); !strings.Contains(data, `"text":""`) {
		t.Fatalf("repeated log: %s", data)
	}
	if data := nextData(); data != `data: "succeeded"` {
		t.Fatalf("missing completion: %s", data)
	}
}

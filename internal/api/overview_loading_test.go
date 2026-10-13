package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestOverviewBoundsStageHistoryAndKeepsActiveRuns(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, resource, pending, pendingStage := approvalFixture(t, a, "history", now.Add(-48*time.Hour))
	active := map[string]string{pendingStage.ID: pending.ID}
	for _, state := range []string{"queued", "running"} {
		revision := core.WorkflowRevision{ID: "old-" + state, ResourceID: resource.ID, State: state, CreatedAt: now.Add(-24 * time.Hour)}
		if err := a.store.CreateWorkflowRevision(ctx, revision); err != nil {
			t.Fatal(err)
		}
		stage := core.WorkflowStageRun{ID: "stage-" + state, RevisionID: revision.ID, State: state, CreatedAt: revision.CreatedAt}
		if err := a.store.CreateWorkflowStageRun(ctx, stage); err != nil {
			t.Fatal(err)
		}
		active[stage.ID] = revision.ID
	}
	for i := 0; i < 1500; i++ {
		revision := core.WorkflowRevision{ID: fmt.Sprintf("history-%04d", i), ResourceID: resource.ID, State: "succeeded", CreatedAt: now.Add(time.Duration(i) * time.Second)}
		if err := a.store.CreateWorkflowRevision(ctx, revision); err != nil {
			t.Fatal(err)
		}
		if err := a.store.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: revision.ID + "-stage", RevisionID: revision.ID, State: "succeeded", CreatedAt: revision.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	a.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != 200 {
		t.Fatalf("overview status %d", response.Code)
	}
	var overview core.Overview
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.WorkflowStageRuns) != 103 {
		t.Fatalf("expected 100 recent and 3 active stages, got %d", len(overview.WorkflowStageRuns))
	}
	revisions := map[string]bool{}
	for _, revision := range overview.WorkflowRevisions {
		revisions[revision.ID] = true
	}
	for _, stage := range overview.WorkflowStageRuns {
		if !revisions[stage.RevisionID] {
			t.Fatalf("stage %s has no revision context", stage.ID)
		}
		delete(active, stage.ID)
	}
	if len(active) != 0 {
		t.Fatalf("old active runs disappeared: %v", active)
	}
	history := httptest.NewRecorder()
	a.ServeHTTP(history, tokenRequest(http.MethodGet, "/api/v1/workflow/revisions/history-0000/stages", nil))
	var stages []core.WorkflowStageRun
	if history.Code != 200 {
		t.Fatalf("history status %d", history.Code)
	}
	if err := json.Unmarshal(history.Body.Bytes(), &stages); err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 || stages[0].ID != "history-0000-stage" {
		t.Fatal("old stage history was lost")
	}
}

func TestOverviewCompressionPreservesWatchBaseline(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	plain := httptest.NewRecorder()
	handler.ServeHTTP(plain, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	request := tokenRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Accept-Encoding", "gzip")
	compressed := httptest.NewRecorder()
	handler.ServeHTTP(compressed, request)
	if compressed.Code != 200 || compressed.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("overview was not compressed")
	}
	compressedSize := compressed.Body.Len()
	reader, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != plain.Body.String() || compressed.Header().Get("X-Overview-Version") != plain.Header().Get("X-Overview-Version") {
		t.Fatal("compression changed overview or watch baseline")
	}
	if compressedSize >= plain.Body.Len() {
		t.Fatal("compression did not reduce transfer")
	}
	if compressed.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("overview can be cached")
	}
}

package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
)

func TestWorkflowPreviewReportListsDeployedSourcesAndImages(t *testing.T) {
	revision := core.WorkflowRevision{ID: "revision-1", Sources: map[string]core.WorkflowSourceRevision{
		"service": {Alias: "service", Repository: "Example/service", Branch: "feature", CommitSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		"ui":      {Alias: "ui", Repository: "Example/ui", Branch: "main", CommitSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}, Outputs: map[string]map[string]string{"build-service": {"imageRepository": "service", "imageTag": "aaaaaaa"}, "build-ui": {"image": "ui:bbbbbbb"}}}
	stages := []core.WorkflowStageRun{{TargetRef: "dev", DeploymentResults: []core.WorkflowDeploymentResult{{DeploymentName: "preview2", Outcome: "deployed"}}}}
	body := workflowPreviewReportBody(revision, core.WorkflowResource{Name: "dev-preview-2"}, stages, "https://preview.example.test", "https://github.example.com", core.HelmPullRequest{Repository: "Example/ui", Number: 123})
	for _, want := range []string{"https://preview.example.test", "dev", "preview2", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "service:aaaaaaa", "ui:bbbbbbb", "https://github.example.com/Example/service/commit/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "https://github.example.com/Example/ui/pull/123"} {
		if !strings.Contains(body, want) {
			t.Fatalf("preview report omitted %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{"**Dispatch revision:**", "**Dispatch controller:**"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("preview report included %q: %s", unwanted, body)
		}
	}
}

func TestWorkflowPreviewFallbackUpdatesExistingComment(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/org/service/issues/comments/123" {
			t.Errorf("unexpected comment request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") == "Bearer app" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"Resource not accessible by integration"}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer operator" {
			t.Errorf("unexpected fallback authorization")
		}
		_, _ = io.WriteString(w, `{"id":123}`)
	}))
	defer server.Close()
	id, err := postWorkflowPreviewComment(context.Background(), events.GitHubNotifier{BaseURL: server.URL, Token: "app"},
		"operator", "org/service", 42, "123", "preview ready")
	if err != nil || id != "123" || requests != 2 {
		t.Fatalf("fallback did not update the existing comment: id=%q requests=%d err=%v", id, requests, err)
	}
}

func TestWorkflowPreviewCommandHelpUsesConfiguredSourcesAndCommand(t *testing.T) {
	revision := core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{
		"service": {Repository: "Example/service"}, "ui": {Repository: "Example/ui"}, "gitops": {Repository: "Example/charts"},
	}}
	for _, origin := range []string{"service", "ui"} {
		trigger := core.WorkflowPreviewTrigger{Repository: "example/" + origin, Command: "/try"}
		body := workflowPreviewCommandHelp(revision, trigger)
		other := "ui"
		if origin == "ui" {
			other = "service"
		}
		for _, want := range []string{"### Preview commands", "`/try`", "/try with " + other + "=#<PR_NUMBER>", "/try with gitops=#<PR_NUMBER>", "<details>", "Saved links remain attached", ",gitops=#<PR_NUMBER>"} {
			if !strings.Contains(body, want) {
				t.Fatalf("missing %q: %s", want, body)
			}
		}
		for _, unwanted := range []string{"with " + origin + "=", "/preview", "/try help", "/try delete", "/try status"} {
			if strings.Contains(body, unwanted) {
				t.Fatalf("unsupported command %q: %s", unwanted, body)
			}
		}
	}
	body := workflowPreviewCommandHelp(core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{"service": {Repository: "org/service"}}}, core.WorkflowPreviewTrigger{Repository: "org/service"})
	if !strings.Contains(body, "`/preview`") || strings.Contains(body, "/preview with ") || strings.Contains(body, "<details>") {
		t.Fatalf("single-source help is misleading: %s", body)
	}
}

func TestWorkflowPreviewReportHelpRetainsLinkedPRsAndTemplateCommit(t *testing.T) {
	revision := core.WorkflowRevision{Sources: map[string]core.WorkflowSourceRevision{"service": {Repository: "org/service"}, "ui": {Repository: "org/ui"}}}
	trigger := core.WorkflowPreviewTrigger{Repository: "org/service", Command: "/preview", LinkedPullRequests: map[string]int{"ui": 84}, TemplateSource: &core.WorkflowPreviewTemplateGitSource{Repository: "org/devops", Path: "deployment/templates/app.yaml", CommitSHA: "abc123"}}
	body := workflowPreviewReportForTrigger(revision, core.WorkflowResource{Name: "preview-42"}, nil, "https://preview.example.test", "https://github.example", trigger)
	for _, want := range []string{"https://github.example/org/ui/pull/84", "[`org/devops/deployment/templates/app.yaml`](https://github.example/org/devops/blob/abc123/deployment/templates/app.yaml) at `abc123`", "### Preview commands", "/preview with ui=#<PR_NUMBER>"} {
		if !strings.Contains(body, want) {
			t.Fatalf("report omitted %q: %s", want, body)
		}
	}
}

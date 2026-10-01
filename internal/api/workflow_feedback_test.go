package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/workflow"
)

type feedbackFixture struct {
	a             *API
	revision      core.WorkflowRevision
	statuses      []map[string]string
	reviews       []map[string]string
	stale         bool
	forbidden     bool
	lookupFailure bool
	draft         bool
	closed        bool
	reviewFailure bool
	crashWindow   bool
	spoof         bool
	dismissed     bool
}

func newFeedbackFixture(t *testing.T) *feedbackFixture {
	t.Helper()
	f := &feedbackFixture{a: serviceTestAPI(t)}
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			fmt.Fprint(w, `{"id":73,"app_id":42}`)
		case r.URL.Path == "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "fixture-app-token", "expires_at": time.Now().Add(time.Hour)})
		case strings.Contains(r.URL.Path, "/statuses/"):
			if r.Header.Get("Authorization") != "Bearer fixture-app-token" {
				t.Error("did not use the installation token")
			}
			if f.forbidden {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
				return
			}
			payload := map[string]string{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			payload["path"] = r.URL.Path
			f.statuses = append(f.statuses, payload)
			fmt.Fprint(w, `{}`)
		case strings.Contains(r.URL.Path, "/issues/"):
			fmt.Fprint(w, `{"id":500}`)
		case strings.HasSuffix(r.URL.Path, "/reviews"):
			if r.Method == http.MethodGet {
				if f.spoof {
					fmt.Fprintf(w, `[{"id":99,"commit_id":%q,"state":"APPROVED","body":%q,"user":{"login":"member","type":"User"}}]`, strings.Repeat("a", 40), "<!-- dispatch-preview-review:"+f.revision.ID+" -->")
				} else if f.crashWindow || f.dismissed {
					state := "APPROVED"
					if f.dismissed {
						state = "DISMISSED"
					}
					fmt.Fprintf(w, `[{"id":99,"commit_id":%q,"state":%q,"body":%q,"user":{"login":"dispatch-test[bot]","type":"Bot"}}]`, strings.Repeat("a", 40), state, "<!-- dispatch-preview-review:"+f.revision.ID+" -->")
				} else {
					fmt.Fprint(w, `[]`)
				}
				return
			}
			if f.reviewFailure {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, `{"message":"Review not accessible"}`)
				return
			}
			payload := map[string]string{}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			payload["path"] = r.URL.Path
			f.reviews = append(f.reviews, payload)
			fmt.Fprint(w, `{"id":99}`)
		case strings.Contains(r.URL.Path, "/pulls/"):
			if f.lookupFailure {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			sha := strings.Repeat("a", 40)
			if strings.Contains(r.URL.Path, "/ui/") {
				sha = strings.Repeat("b", 40)
				if f.stale {
					sha = strings.Repeat("c", 40)
				}
			}
			state := "open"
			if f.closed {
				state = "closed"
			}
			repo := "example/service"
			if strings.Contains(r.URL.Path, "/ui/") {
				repo = "example/ui"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "draft": f.draft, "head": map[string]any{"sha": sha, "ref": "feature", "repo": map[string]any{"id": 101, "full_name": repo}}, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": 101, "full_name": repo}}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	manager := githubapp.New(f.a.store, f.a.eventConfig.Vault)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	encrypted, webhook, err := manager.EncryptCredentials("qa-app", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "0123456789abcdef")
	must(err)
	now := time.Now().UTC()
	must(f.a.store.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "qa-app", Name: "QA App", Slug: "dispatch-test", AppID: 42, InstallationID: 73, APIURL: server.URL, WebURL: server.URL, EncryptedPrivateKey: encrypted, EncryptedWebhookSecret: webhook, State: "ready", CreatedAt: now, UpdatedAt: now}))
	f.a.eventConfig.GitHubApps = manager
	f.a.auth.PublicURL = "https://dispatch.example.test"
	projects, err := f.a.store.ListProjects(ctx)
	must(err)
	must(f.a.store.CreateConfigSource(ctx, core.ConfigSource{ID: "qa-config", GitHubAppID: "qa-app", ProjectID: projects[0].ID, Name: "QA", Repository: "example/config", Active: true, CreatedAt: now, UpdatedAt: now}))
	must(f.a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "qa-preview", ConfigSourceID: "qa-config", Kind: "Application", Name: "preview-42", Active: true, Temporary: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	f.revision = core.WorkflowRevision{ID: "qa-run-1", ResourceID: "qa-preview", State: "running", Trigger: "pull request test 1", CreatedAt: now,
		Feedback: &core.WorkflowFeedback{DeploymentID: "deployed", PreviewURL: "https://preview.example.test", WorkflowReporting: core.WorkflowReporting{StatusContext: "Dispatch/qa", ReviewOnSuccess: "approve", ReviewOnFailure: "requestChanges"}, Targets: []core.WorkflowFeedbackTarget{
			{WorkflowPullRequest: core.WorkflowPullRequest{GitHubAppID: "qa-app", Repository: "example/service", Number: 42, CommitSHA: strings.Repeat("a", 40)}},
			{WorkflowPullRequest: core.WorkflowPullRequest{GitHubAppID: "qa-app", Repository: "example/ui", Number: 84, CommitSHA: strings.Repeat("b", 40)}},
		}}}
	must(f.a.store.CreateWorkflowRevision(ctx, f.revision))
	return f
}

func TestWorkflowFeedbackReportsPendingAndExactTestedPRs(t *testing.T) {
	for _, outcome := range []string{"succeeded", "failed", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			f := newFeedbackFixture(t)
			ctx := context.Background()
			if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
				t.Fatal(err)
			}
			if len(f.statuses) != 2 || len(f.reviews) != 0 {
				t.Fatalf("unexpected pending feedback: %+v %+v", f.statuses, f.reviews)
			}
			for _, status := range f.statuses {
				if status["state"] != "pending" || status["context"] != "Dispatch/qa" || status["target_url"] != "https://dispatch.example.test/events?run=qa-run-1" {
					t.Fatalf("unexpected status: %+v", status)
				}
			}
			f.revision.State = outcome
			if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
				t.Fatal(err)
			}
			// Recreate the API to exercise persistent progress, rather than local caches.
			a := New(f.a.store, f.a.deploy, false, f.a.auth, f.a.logger, f.a.eventConfig)
			if err := a.reconcileWorkflowFeedback(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.reconcileWorkflowFeedback(ctx); err != nil {
				t.Fatal(err)
			}
			want := "success"
			event := "APPROVE"
			if outcome == "failed" {
				want = "failure"
				event = "REQUEST_CHANGES"
			}
			if outcome == "cancelled" {
				want = "error"
			}
			if len(f.statuses) != 4 {
				t.Fatalf("status duplicate or missing: %+v", f.statuses)
			}
			for _, status := range f.statuses[2:] {
				if status["state"] != want {
					t.Fatalf("wrong terminal state: %+v", status)
				}
			}
			if outcome == "cancelled" {
				if len(f.reviews) != 0 {
					t.Fatal("reviewed a cancelled check")
				}
				return
			}
			if len(f.reviews) != 2 {
				t.Fatalf("did not review both linked PRs exactly once: %+v", f.reviews)
			}
			for i, review := range f.reviews {
				sha := strings.Repeat("a", 40)
				if i == 1 {
					sha = strings.Repeat("b", 40)
				}
				if review["commit_id"] != sha || review["event"] != event {
					t.Fatalf("review did not use tested commits: %+v", review)
				}
			}
		})
	}
}

func TestWorkflowFeedbackDoesNotReviewStaleDraftClosedOrDisabledPRs(t *testing.T) {
	for _, condition := range []string{"stale", "draft", "closed", "disabled", "superseded"} {
		t.Run(condition, func(t *testing.T) {
			f := newFeedbackFixture(t)
			ctx := context.Background()
			f.stale = condition == "stale"
			f.draft = condition == "draft"
			f.closed = condition == "closed"
			if condition == "disabled" {
				f.revision.Feedback.ReviewOnSuccess = ""
				f.revision.Feedback.ReviewOnFailure = ""
				if err := f.a.store.UpdateWorkflowFeedback(ctx, f.revision.ID, f.revision.Feedback); err != nil {
					t.Fatal(err)
				}
			}
			if condition == "superseded" {
				next := f.revision
				next.ID = "qa-run-2"
				next.CreatedAt = next.CreatedAt.Add(time.Second)
				next.Feedback = nil
				next.Trigger = "pull request comment 2"
				if err := f.a.store.CreateWorkflowRevision(ctx, next); err != nil {
					t.Fatal(err)
				}
			}
			f.revision.State = "succeeded"
			if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
				t.Fatal(err)
			}
			if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
				t.Fatal(err)
			}
			if len(f.reviews) != 0 {
				t.Fatalf("reviewed unsafe/disabled result: %+v", f.reviews)
			}
			result, err := f.a.store.GetWorkflowRevision(ctx, f.revision.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Feedback.Complete {
				t.Fatal("skipped results remain pending")
			}
			for _, target := range result.Feedback.Targets {
				if condition != "disabled" && target.Review != "skipped" {
					t.Fatalf("skip reason missing: %+v", target)
				}
			}
		})
	}
}

func TestWorkflowFeedbackRetriesPermissionFailuresWithoutPersonalToken(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	f.forbidden = true
	f.a.eventConfig.GitHubToken = "operator-token-must-not-be-used"
	f.revision.State = "succeeded"
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err == nil || !strings.Contains(err.Error(), "Commit statuses: write") {
		t.Fatalf("missing actionable error: %v", err)
	}
	stored, err := f.a.store.GetWorkflowRevision(ctx, f.revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Feedback.Complete || stored.Feedback.Targets[0].Error == "" {
		t.Fatal("reporting error was not persisted")
	}
	f.forbidden = false
	f.reviewFailure = true
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err == nil || !strings.Contains(err.Error(), "Pull requests: write") {
		t.Fatalf("missing review permission error: %v", err)
	}
	f.reviewFailure = false
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
		t.Fatal(err)
	}
	if len(f.statuses) != 2 || len(f.reviews) != 2 {
		t.Fatalf("retry duplicated status or omitted review: %+v %+v", f.statuses, f.reviews)
	}
}

func TestWorkflowFeedbackRecoversReviewPostBeforeDatabaseWrite(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	f.crashWindow = true
	f.revision.State = "succeeded"
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews) != 1 || strings.Contains(f.reviews[0]["path"], "/service/") {
		t.Fatalf("successful service review was posted twice: %+v", f.reviews)
	}
}

func TestWorkflowFeedbackLookupFailureRemainsRetryable(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	f.lookupFailure = true
	f.revision.State = "succeeded"
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err == nil {
		t.Fatal("lookup failure ignored")
	}
	stored, _ := f.a.store.GetWorkflowRevision(ctx, f.revision.ID)
	if stored.Feedback.Complete || stored.Feedback.Targets[0].Error == "" || len(f.statuses) != 2 {
		t.Fatal("head lookup prevented status reporting or lost its error")
	}
	f.lookupFailure = false
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
		t.Fatal(err)
	}
	if len(f.statuses) != 2 || len(f.reviews) != 2 {
		t.Fatal("retry was not idempotent")
	}
}

func TestPreviewCommentQAReportsStatusesAndReviewsEndToEnd(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Seed a ready deployment, then exercise the real comment and Pipeline path.
	must(f.a.store.UpdateWorkflowFeedback(ctx, f.revision.ID, nil))
	f.revision.State = "cancelled"
	must(f.a.store.UpdateWorkflowRevision(ctx, f.revision))
	resource, err := f.a.store.GetWorkflowResource(ctx, "qa-preview")
	must(err)
	resource.Path = "temporary/preview.yaml"
	resource.Document = `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: preview-42}
spec:
  reporting: {reviewOnSuccess: approve, reviewOnFailure: requestChanges}
  sources: {service: {repository: example/service, branch: main}, ui: {repository: example/ui, branch: main}}
  deployments:
    app:
      helm: {sourceRef: service, chartPath: chart}
  stages:
    - name: development
      targetRef: dev
      deploy: [app]
      url: https://preview.example.test
      checks:
        qa: {pipelineRef: qa-endpoints, when: onDemand, with: {base-url: "{{ stage.url }}"}}
`
	docs, err := workflow.Parse(resource.Path, []byte(resource.Document))
	must(err)
	resource.SpecDigest, err = docs[0].Digest()
	must(err)
	must(f.a.store.UpdateWorkflowResource(ctx, resource))
	now := time.Now().UTC()
	sources := map[string]core.WorkflowSourceRevision{}
	prs := []core.WorkflowPullRequest{}
	for _, target := range f.revision.Feedback.Targets {
		alias := "service"
		if target.Repository == "example/ui" {
			alias = "ui"
		}
		sources[alias] = core.WorkflowSourceRevision{Alias: alias, Repository: target.Repository, CommitSHA: target.CommitSHA}
		prs = append(prs, target.WorkflowPullRequest)
	}
	deployed := core.WorkflowRevision{ID: "ready-deployment", ResourceID: resource.ID, State: "succeeded", Trigger: "pull request comment original", Sources: sources, PullRequests: prs, SpecDigest: resource.SpecDigest, CreatedAt: now}
	must(f.a.store.CreateWorkflowRevision(ctx, deployed))
	must(f.a.store.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "ready-stage", RevisionID: deployed.ID, StageName: "development", State: "succeeded", CreatedAt: now}))
	pipeline := `apiVersion: dispatch/v1alpha1
kind: Pipeline
metadata: {name: qa-endpoints}
spec:
  inputs: {base-url: {required: true}}
  jobs:
    endpoints:
      run: sleep 0.1; test "{{ inputs.base-url }}" = "https://preview.example.test"
`
	must(f.a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "qa-framework", ConfigSourceID: "qa-config", Kind: "Pipeline", Name: "qa-endpoints", Path: "deployment/qa.yaml", Document: pipeline, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	trigger := core.WorkflowPreviewTrigger{ID: "qa-trigger", ResourceID: resource.ID, GitHubAppID: "qa-app", Repository: "example/service", PullRequestNumber: 42, LinkedPullRequests: map[string]int{"ui": 84}, Command: "/preview", PreviewURL: "https://preview.example.test", CreatedAt: now}
	must(f.a.store.CreateWorkflowPreviewTrigger(ctx, trigger))
	event := core.IncomingEvent{ProviderConnectionID: "qa-app", Repository: "example/service", PullRequestNumber: 42, Command: "/preview", Arguments: "test", SourceCommentID: "test-command", TrustedActor: true}
	target := &previewPollTarget{connectionID: "qa-app", repository: "example/service", workflowTriggers: []core.WorkflowPreviewTrigger{trigger}}
	must(f.a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}))
	must(f.a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}))
	runID, err := f.a.store.WorkflowPreviewCommentRevision(ctx, trigger.ID, event.SourceCommentID)
	must(err)
	var run core.WorkflowRevision
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err = f.a.store.GetWorkflowRevision(ctx, runID)
		must(err)
		if run.State == "succeeded" || run.State == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.State != "succeeded" {
		t.Fatalf("QA pipeline failed: %+v", run)
	}
	must(f.a.reportPendingWorkflowPreviews(ctx, trigger))
	must(f.a.reportPendingWorkflowPreviews(ctx, trigger))
	if len(f.reviews) != 2 {
		t.Fatalf("real comment path did not review both tested PRs: %+v", f.reviews)
	}
	for _, review := range f.reviews {
		if review["event"] != "APPROVE" {
			t.Fatalf("wrong review: %+v", review)
		}
	}
	stages, err := f.a.store.ListWorkflowStageRuns(ctx, runID)
	must(err)
	if len(stages) != 1 || stages[0].CheckRuns["qa"] == "" || len(stages[0].DeploymentIDs) != 0 {
		t.Fatalf("QA command deployed or omitted test pipeline: %+v", stages)
	}
	for _, status := range f.statuses {
		if strings.Contains(status["path"], "example/config") {
			t.Fatal("reported QA on GitOps commit")
		}
	}
}

func TestWorkflowFeedbackRejectsSpoofedReviewMarker(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	f.spoof = true
	f.revision.State = "succeeded"
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews) != 2 {
		t.Fatal("a user-authored marker impersonated the App review")
	}
}

func TestWorkflowFeedbackDoesNotRequestChangesAfterControllerRecovery(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
		t.Fatal(err)
	}
	f.revision.State = "failed"
	f.revision.Error = core.WorkflowInterruptedMessage
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reconcileWorkflowFeedback(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.reviews) != 0 {
		t.Fatal("controller recovery requested changes on a PR")
	}
	for _, status := range f.statuses[2:] {
		if status["state"] != "error" {
			t.Fatalf("interrupted QA reported a verdict: %+v", status)
		}
	}
	stored, err := f.a.store.GetWorkflowRevision(ctx, f.revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range stored.Feedback.Targets {
		if target.Review != "skipped" || target.SkipReason == "" {
			t.Fatal("interruption reason is missing")
		}
	}
	if !strings.Contains(workflowPreviewTestReport(stored, nil, "https://preview.example.test"), "checks interrupted") {
		t.Fatal("interrupted check was reported as a test failure")
	}
}

func TestWorkflowFeedbackDoesNotRecreateDismissedReviewOnRetry(t *testing.T) {
	f := newFeedbackFixture(t)
	ctx := context.Background()
	f.dismissed = true
	f.revision.State = "succeeded"
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	if err := f.a.reportWorkflowFeedback(ctx, "qa-preview"); err != nil {
		t.Fatal(err)
	}
	stored, err := f.a.store.GetWorkflowRevision(ctx, f.revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Feedback.Targets[0].Review != "dismissed" || stored.Feedback.Targets[0].ReviewID != 99 {
		t.Fatal("dismissal was not preserved")
	}
	if len(f.reviews) != 1 || strings.Contains(f.reviews[0]["path"], "/service/") {
		t.Fatal("retry replaced a dismissed review")
	}
}

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestPreviewSourceTrustApprovalAPIAndDirectRun(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	a := handler.(*API)
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	projects, err := a.store.ListProjects(ctx)
	must(err)
	must(a.store.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "trust-github", Name: "Trust GitHub", CreatedAt: now, UpdatedAt: now}))
	source := core.ConfigSource{ID: "trust-source", ProjectID: projects[0].ID, GitHubAppID: "trust-github", Name: "Trust source", Repository: "example/config", Active: true, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateConfigSource(ctx, source))
	raw := `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: trust-preview}
spec:
  sources:
    app: {repository: example/app, ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
`
	docs, err := workflow.Parse("temporary/trust.yaml", []byte(raw))
	must(err)
	digest, err := docs[0].Digest()
	must(err)
	resource := core.WorkflowResource{ID: "trust-resource", ConfigSourceID: source.ID, Kind: workflow.KindApplication, Name: "trust-preview", Path: "temporary/trust.yaml", Document: raw, SpecDigest: digest, Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateWorkflowResource(ctx, resource))
	must(a.store.CreateWorkflowPreviewTrigger(ctx, core.WorkflowPreviewTrigger{ID: "trust-trigger", ResourceID: resource.ID, GitHubAppID: "trust-github", Repository: "example/app", PullRequestNumber: 1, Command: "/preview", CreatedAt: now}))
	revision := core.WorkflowRevision{ID: "trust-revision", ResourceID: resource.ID, SpecDigest: digest, State: "failed", Sources: map[string]core.WorkflowSourceRevision{"app": {Alias: "app", Repository: "example/app", Branch: strings.Repeat("a", 40), CommitSHA: strings.Repeat("a", 40)}}, CreatedAt: now}
	must(a.store.CreateWorkflowRevision(ctx, revision))
	head := githubapp.PullRequestHead{State: "open"}
	head.Head.SHA = strings.Repeat("a", 40)
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 2, FullName: "outsider/app", Fork: true}
	head.Base.Repo = &githubapp.PullRequestRepository{ID: 1, FullName: "example/app"}
	a.workflows.ResolvePreviewSource = func(context.Context, string, string, int) (githubapp.PullRequestHead, error) { return head, nil }
	request := func(method, path, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/workflow/revisions/" + revision.ID + "/source-trust"
	reviewResponse := request("GET", path, "secret", nil)
	if reviewResponse.Code != 200 {
		t.Fatal(reviewResponse.Code, reviewResponse.Body.String())
	}
	var review core.PreviewSourceTrustDecision
	must(json.Unmarshal(reviewResponse.Body.Bytes(), &review))
	if review.Allowed || review.Digest == "" || !review.Sources[0].Fork {
		t.Fatalf("bad review: %+v", review)
	}
	body := map[string]any{"confirmDigest": review.Digest, "expiresAt": now.Add(time.Hour)}
	member := core.User{ID: "trust-member", Username: "trust-member", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateUser(ctx, member))
	memberToken, e := a.createSession(ctx, member.ID, identityForUser(member))
	must(e)
	if response := request("POST", path+"/approvals", memberToken, body); response.Code != http.StatusForbidden {
		t.Fatalf("non-owner approved fork code: %d", response.Code)
	}

	if response := request("POST", path+"/approvals", "", body); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated approval: %d", response.Code)
	}
	body["confirmDigest"] = "stale"
	if response := request("POST", path+"/approvals", "secret", body); response.Code != 409 {
		t.Fatalf("stale digest approved: %d %s", response.Code, response.Body.String())
	}
	body["confirmDigest"] = review.Digest
	body["expiresAt"] = now.Add(-time.Second)
	if response := request("POST", path+"/approvals", "secret", body); response.Code != 422 {
		t.Fatalf("expired approval accepted: %d", response.Code)
	}
	body["expiresAt"] = now.Add(time.Hour)
	approved := request("POST", path+"/approvals", "secret", body)
	if approved.Code != 201 {
		t.Fatal(approved.Code, approved.Body.String())
	}
	var approval core.PreviewSourceTrustApproval
	must(json.Unmarshal(approved.Body.Bytes(), &approval))
	decision, err := a.workflows.SourceTrust(ctx, resource, revision)
	if err != nil || !decision.Allowed {
		t.Fatalf("approved source denied: %+v %v", decision, err)
	}
	head.Head.SHA = strings.Repeat("b", 40)
	if response := request("POST", path+"/approvals", "secret", body); response.Code != 409 {
		t.Fatalf("changed head approved: %d %s", response.Code, response.Body.String())
	}
	head.Head.SHA = strings.Repeat("a", 40)
	if response := request("DELETE", path+"/approvals/"+approval.ID, "secret", nil); response.Code != 204 {
		t.Fatalf("revocation failed: %d %s", response.Code, response.Body.String())
	}
	// Direct API runs pass through the same service gate as comment/polling runs.
	response := request("POST", "/api/v1/workflow/resources/"+resource.ID+"/runs", "secret", nil)
	if response.Code != 422 || !strings.Contains(response.Body.String(), "source trust denied") {
		t.Fatalf("direct run bypassed trust: %d %s", response.Code, response.Body.String())
	}
	triggers, e := a.store.ListWorkflowPreviewTriggers(ctx)
	must(e)
	trigger := triggers[0]
	target := &previewPollTarget{connectionID: "trust-github", repository: "example/app", workflowTriggers: []core.WorkflowPreviewTrigger{trigger}}
	event := core.IncomingEvent{Kind: core.EventKindPullRequestComment, Action: "created", ProviderConnectionID: "trust-github", Repository: "example/app", PullRequestNumber: 1, Command: "/preview", SourceCommentID: "321", HeadSHA: head.Head.SHA, TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, target, event, events.GitHubResolver{}); !errors.Is(err, workflow.ErrPreviewSourceTrust) {
		t.Fatalf("trusted comment bypassed source trust: %v", err)
	}
	head.Head.SHA = strings.Repeat("b", 40)
	trigger.AutoDeploy = true
	if err := a.updateWorkflowPreviewHead(ctx, trigger, trigger.Repository, head.Head.SHA, events.GitHubResolver{}, "poll"); !errors.Is(err, workflow.ErrPreviewSourceTrust) {
		t.Fatalf("polling bypassed source trust: %v", err)
	}
	runs, e := a.store.ListWorkflowRevisions(ctx, resource.ID, 0)
	must(e)
	for _, run := range runs {
		jobs, e := a.store.ListWorkflowJobResults(ctx, run.ID)
		must(e)
		if len(jobs) != 0 {
			t.Fatal("untrusted API/comment/poll run dispatched jobs")
		}
	}

}

// Unrelated lifecycle tests use an explicit provider metadata fixture rather
// than silently treating absent source identities as trusted.
func fixturePreviewSource(repository, sha string) githubapp.PullRequestHead {
	head := githubapp.PullRequestHead{State: "open"}
	head.Head.SHA = sha
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 101, FullName: repository}
	head.Base.Repo = &githubapp.PullRequestRepository{ID: 101, FullName: repository}
	return head
}

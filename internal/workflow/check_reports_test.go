package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestWorkflowCheckPlansCaptureSourcesAndFenceInterruptedCreates(t *testing.T) {
	data, source, resource := workflowRunnerFixture(t, "checks")
	ctx := context.Background()
	now := time.Now().UTC()
	service := &Service{Store: data}
	resource.Document = `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: storefront
spec:
  sources:
    chart: {repository: example/chart}
  deployments:
    app:
      helm: {sourceRef: chart}
  stages:
    - name: preview
      targetRef: target
      deploy: [app]
      checks:
        ready: {pipelineRef: readiness}
        e2e: {pipelineRef: browser-tests, when: onDemand}
`
	linked := core.GitHubAppConnection{ID: "linked-app", AppID: 27, Name: "Linked", APIURL: "https://github.linked.test/api/v3", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateGitHubApp(ctx, linked); err != nil {
		t.Fatal(err)
	}
	revision := core.WorkflowRevision{ID: "accepted-check-run", ResourceID: resource.ID, State: "queued", CreatedAt: now, Sources: map[string]core.WorkflowSourceRevision{
		"service":      {Repository: "example/service", CommitSHA: strings.Repeat("a", 40)},
		"same-service": {Repository: "example/service", CommitSHA: strings.Repeat("a", 40)},
		"ui":           {Repository: "example/ui", CommitSHA: strings.Repeat("b", 40)},
	}, PullRequests: []core.WorkflowPullRequest{{GitHubAppID: linked.ID, Repository: "example/ui", CommitSHA: strings.Repeat("b", 40), Number: 2}}}
	reports, err := service.workflowCheckReports(ctx, resource, source, revision, false)
	if err != nil || len(reports) != 4 {
		t.Fatalf("deduped plans: %+v %v", reports, err)
	}
	for _, r := range reports {
		if r.Kind == "health" && r.Check != "ready" {
			t.Fatal("scheduled on-demand health reporting")
		}
		if r.Repository == "example/ui" && (r.GitHubAppID != linked.ID || r.AppID != 27 || r.CommitSHA != strings.Repeat("b", 40)) {
			t.Fatalf("linked source identity lost: %+v", r)
		}
	}
	qa, err := service.workflowCheckReports(ctx, resource, source, revision, true)
	if err != nil || len(qa) != 2 || qa[0].Kind != "qa" {
		t.Fatalf("QA duplicated health checks %+v %v", qa, err)
	}
	// Invalid receipt persistence must roll back the revision too.
	invalid := revision
	invalid.ID = "invalid-accepted-run"
	invalid.Checks = []core.WorkflowCheckReport{reports[0]}
	if err := data.CreateWorkflowRevision(ctx, invalid); err == nil {
		t.Fatal("accepted invalid report identity")
	}
	if _, err := data.GetWorkflowRevision(ctx, invalid.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("partial revision survived: %v", err)
	}
	revision.Checks = reports
	if err := data.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	saved, err := data.ListWorkflowChecks(ctx, revision.ID)
	if err != nil || len(saved) != 4 {
		t.Fatalf("plans not atomic %+v %v", saved, err)
	}
	first, err := data.ClaimWorkflowCheck(ctx, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.BeginWorkflowCheckCreate(ctx, first, now); err != nil {
		t.Fatal(err)
	}
	// Other due rows exist, but no second lease may claim this receipt.
	second, err := data.ClaimWorkflowCheck(ctx, now, time.Minute)
	if err != nil || second.ID == first.ID {
		t.Fatalf("duplicate lease %+v %v", second, err)
	}
	if err := data.FinishWorkflowCheck(ctx, first, now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired worker overwrote receipt: %v", err)
	}
	// A restarted worker recovers the durable create intent even though its prior
	// owner never saved the network result. It must not issue another POST.
	claimed, err := data.ClaimWorkflowCheck(ctx, now.Add(2*time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != first.ID || claimed.CreateState != "posting" {
		t.Fatalf("lost intent: %+v", claimed)
	}
	if err := data.BeginWorkflowCheckCreate(ctx, claimed, now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("recreated unknown receipt: %v", err)
	}
	claimed.CheckID = 99
	claimed.State = "reported"
	claimed.Complete = true
	claimed.CreateState = "created"
	if err := data.FinishWorkflowCheck(ctx, claimed, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	first.Complete = false
	first.CreateState = ""
	if err := data.FinishWorkflowCheck(ctx, first, now.Add(2*time.Minute)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale receipt reset accepted ID: %v", err)
	}
	// A separate logical workflow run gets a distinct identity on the same commit.
	revision.ID = "new-run"
	next, err := service.workflowCheckReports(ctx, resource, source, revision, false)
	if err != nil || next[0].ExternalID == reports[0].ExternalID {
		t.Fatalf("new run reused identity: %+v %v", next, err)
	}
}

package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
)

func sourceTrustFixture(t *testing.T) (workflowNoopFixture, *map[string]githubapp.PullRequestHead) {
	t.Helper()
	f := newWorkflowNoopFixture(t)
	f.snapshot = f.previous.Sources
	ctx := context.Background()
	f.resource.Temporary = true
	if err := f.data.UpdateWorkflowResource(ctx, f.resource); err != nil {
		t.Fatal(err)
	}
	trigger := core.WorkflowPreviewTrigger{ID: "trust-trigger", ResourceID: f.resource.ID, GitHubAppID: f.source.GitHubAppID, Repository: "example/service", PullRequestNumber: 42, LinkedPullRequests: map[string]int{"gitops": 7}, PreviewURL: "https://preview.example/42", CreatedAt: time.Now().UTC()}
	if err := f.data.CreateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	heads := map[string]githubapp.PullRequestHead{}
	for alias, pinned := range f.snapshot {
		head := githubapp.PullRequestHead{State: "open"}
		head.Head.SHA = pinned.CommitSHA
		id := int64(12)
		if alias == "gitops" {
			id = 13
		}
		head.Base.Repo = &githubapp.PullRequestRepository{ID: id, FullName: pinned.Repository}
		head.Head.Repo = &githubapp.PullRequestRepository{ID: id, FullName: pinned.Repository}
		heads[pinned.Repository] = head
	}
	f.service.ResolvePreviewSource = func(_ context.Context, _ string, repository string, _ int) (githubapp.PullRequestHead, error) {
		head, ok := heads[normalizeRepository(repository)]
		if !ok {
			return head, errors.New("unavailable")
		}
		return head, nil
	}
	return f, &heads
}

func TestPreviewTrustChecksPrimaryAndLinkedOrigins(t *testing.T) {
	for _, repository := range []string{"example/service", "example/gitops"} {
		t.Run(repository, func(t *testing.T) {
			f, heads := sourceTrustFixture(t)
			revision := f.previous
			revision.ResourceID = f.resource.ID
			decision, err := f.service.SourceTrust(context.Background(), f.resource, revision)
			if err != nil || !decision.Allowed || len(decision.Sources) != 2 {
				t.Fatalf("same-repository sources denied: %+v %v", decision, err)
			}
			head := (*heads)[repository]
			head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outsider/fork", Fork: true}
			(*heads)[repository] = head
			decision, err = f.service.SourceTrust(context.Background(), f.resource, revision)
			if !errors.Is(err, ErrPreviewSourceTrust) || decision.Allowed || decision.Digest == "" {
				t.Fatalf("fork accepted or not reviewable: %+v %v", decision, err)
			}
			for _, origin := range decision.Sources {
				if origin.Repository == repository && !origin.Fork {
					t.Fatal("fork identity missing")
				}
			}
		})
	}
}

func TestPreviewTrustApprovalBindsRevisionScopeAndExpires(t *testing.T) {
	f, heads := sourceTrustFixture(t)
	ctx := context.Background()
	revision := f.previous
	head := (*heads)["example/service"]
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outside/service", Fork: true}
	(*heads)["example/service"] = head
	decision, err := f.service.SourceTrust(ctx, f.resource, revision)
	if !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal(err)
	}
	approval := core.PreviewSourceTrustApproval{ID: "approval", ResourceID: f.resource.ID, Digest: decision.Digest, ActorID: "controller-owner", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour)}
	if err := f.data.CreatePreviewSourceTrustApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	allowed, err := f.service.SourceTrust(ctx, f.resource, revision)
	if err != nil || !allowed.Allowed || allowed.ApprovalID != approval.ID {
		t.Fatalf("matching approval denied: %+v %v", allowed, err)
	}
	t.Run("changed head", func(t *testing.T) {
		saved := head
		head.Head.SHA = strings.Repeat("c", 40)
		(*heads)["example/service"] = head
		defer func() { head = saved; (*heads)["example/service"] = saved }()
		if _, err := f.service.SourceTrust(ctx, f.resource, revision); !errors.Is(err, ErrPreviewSourceTrust) {
			t.Fatal("changed head accepted")
		}
	})
	t.Run("relinked source", func(t *testing.T) {
		triggers, _ := f.data.ListWorkflowPreviewTriggers(ctx)
		trigger := triggers[0]
		trigger.LinkedPullRequests["gitops"] = 8
		if err := f.data.SaveWorkflowPreviewSources(ctx, f.resource, trigger); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.SourceTrust(ctx, f.resource, revision); !errors.Is(err, ErrPreviewSourceTrust) {
			t.Fatal("relinked PR reused approval")
		}
		trigger.LinkedPullRequests["gitops"] = 7
		if err := f.data.SaveWorkflowPreviewSources(ctx, f.resource, trigger); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("changed configuration", func(t *testing.T) {
		changed := f.resource
		changed.SpecDigest = "changed"
		if _, err := f.service.SourceTrust(ctx, changed, revision); !errors.Is(err, ErrPreviewSourceTrust) {
			t.Fatal("changed specification accepted")
		}
	})
	t.Run("changed target credentials", func(t *testing.T) {
		target := f.server
		config := *target.Kubernetes
		config.KubeconfigData = "rotated-target-credential"
		target.Kubernetes = &config
		if err := f.data.UpdateServer(ctx, target); err != nil {
			t.Fatal(err)
		}
		defer f.data.UpdateServer(ctx, f.server)
		decision, err := f.service.SourceTrust(ctx, f.resource, revision)
		if !errors.Is(err, ErrPreviewSourceTrust) || decision.Allowed {
			t.Fatal("changed target reused prior approval")
		}
		for _, scope := range decision.CredentialScope {
			if strings.Contains(scope, config.KubeconfigData) {
				t.Fatal("target credential leaked into approval evidence")
			}
		}
	})
	if err := f.data.RevokePreviewSourceTrustApproval(ctx, f.resource.ID, approval.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SourceTrust(ctx, f.resource, revision); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("revoked approval accepted")
	}
	approval.ID = "expired"
	approval.ExpiresAt = time.Now().Add(-time.Second)
	if err := f.data.CreatePreviewSourceTrustApproval(ctx, approval); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SourceTrust(ctx, f.resource, revision); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("expired approval accepted")
	}
}

func TestPreviewTrustDeniesBeforeSecretResolutionOrDispatch(t *testing.T) {
	f, heads := sourceTrustFixture(t)
	ctx := context.Background()
	head := (*heads)["example/service"]
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outsider/service"}
	(*heads)["example/service"] = head
	// Missing resolver would report credential-resolution failure if reached.
	runtime := &jobRuntime{service: f.service, source: f.source, revision: f.previous, paths: map[string]string{}}
	_, err := runtime.executeJob(ctx, f.resource, "protected", JobSpec{Run: "exit 98", Secrets: map[string]SecretBinding{"TOKEN": {SecretRef: "private-credential"}}}, false)
	if !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatalf("secret resolution ran before trust denial: %v", err)
	}
	before := f.runner.starts
	revision, err := f.service.startWithSnapshot(ctx, f.resource, f.source, f.snapshot, "pull request comment trusted-author")
	if !errors.Is(err, ErrPreviewSourceTrust) || revision.ID == "" {
		t.Fatalf("trusted comment bypassed policy: %+v %v", revision, err)
	}
	saved, err := f.data.GetWorkflowRevision(ctx, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != "failed" || saved.SourceTrust == nil || saved.SourceTrust.Allowed || len(saved.SourceTrust.Sources) != 2 || f.runner.starts != before {
		t.Fatalf("denial was not saved before dispatch: %+v", saved)
	}
	jobs, err := f.data.ListWorkflowJobResults(ctx, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatal("denied run dispatched jobs")
	}
}

func TestPreviewTrustProtectsEquivalentWorkflowComparison(t *testing.T) {
	f, heads := sourceTrustFixture(t)
	head := (*heads)["example/gitops"]
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outsider/gitops"}
	(*heads)["example/gitops"] = head
	if f.service.reuseEquivalentWorkflow(context.Background(), f.resource, f.source, f.snapshot) {
		t.Fatal("untrusted sources reused protected workflow outputs")
	}
	if f.runner.compares != 0 || f.runner.starts != 0 {
		t.Fatal("untrusted preview reached deployment comparison")
	}
}

func TestPreviewTrustDecisionSurvivesRunStatusUpdate(t *testing.T) {
	f, heads := sourceTrustFixture(t)
	head := (*heads)["example/service"]
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outsider/service"}
	(*heads)["example/service"] = head
	ctx := context.Background()
	if err := f.service.checkRevisionTrust(ctx, f.previous); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal(err)
	}
	// The runtime still holds its initial revision value while saving a failure.
	stale := f.previous
	stale.State = "failed"
	if err := f.data.UpdateWorkflowRevision(ctx, stale); err != nil {
		t.Fatal(err)
	}
	saved, err := f.data.GetWorkflowRevision(ctx, stale.ID)
	if err != nil || saved.SourceTrust == nil || saved.SourceTrust.Allowed {
		t.Fatal("run status update overwrote the latest trust evidence", err)
	}
}

func TestPreviewTrustRechecksOwnerAndMissingOrigin(t *testing.T) {
	f, heads := sourceTrustFixture(t)
	ctx := context.Background()
	head := (*heads)["example/service"]
	head.Head.Repo = nil
	(*heads)["example/service"] = head
	decision, err := f.service.SourceTrust(ctx, f.resource, f.previous)
	if !errors.Is(err, ErrPreviewSourceTrust) || decision.Digest != "" {
		t.Fatalf("missing identity is approvable: %+v %v", decision, err)
	}
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outsider/service"}
	(*heads)["example/service"] = head
	decision, _ = f.service.SourceTrust(ctx, f.resource, f.previous)
	now := time.Now().UTC()
	user := core.User{ID: "approver", Username: "approver", SystemRole: core.UserRoleOwner, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err := f.data.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := f.data.CreatePreviewSourceTrustApproval(ctx, core.PreviewSourceTrustApproval{ID: "grant", ResourceID: f.resource.ID, Digest: decision.Digest, ActorID: user.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SourceTrust(ctx, f.resource, f.previous); err != nil {
		t.Fatal(err)
	}
	user.SystemRole = core.UserRoleMember
	if err := f.data.UpdateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SourceTrust(ctx, f.resource, f.previous); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("demoted owner approval remains usable")
	}
}

func TestPreviewTrustAppliesToDirectDeploymentsAndCheckPipelines(t *testing.T) {
	f, heads := sourceTrustFixture(t)
	ctx := context.Background()
	app := core.App{SourceRepo: "https://github.example/example/gitops", HelmProvenance: core.HelmProvenance{WorkflowResourceID: f.resource.ID, WorkflowRevisionID: f.previous.ID}}
	if err := f.service.CheckDeploymentTrust(ctx, app, f.previous.Sources["gitops"].CommitSHA); err != nil {
		t.Fatal(err)
	}
	if err := f.service.CheckDeploymentTrust(ctx, app, "unreviewed-commit"); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("direct deploy changed the chart commit")
	}
	head := (*heads)["example/service"]
	head.Head.Repo = &githubapp.PullRequestRepository{ID: 999, FullName: "outsider/service"}
	(*heads)["example/service"] = head
	if err := f.service.CheckDeploymentTrust(ctx, app, f.previous.Sources["gitops"].CommitSHA); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("direct deployment bypassed a linked fork")
	}
	child := core.WorkflowRevision{ID: "pipeline-child", ResourceID: f.resource.ID, State: "running"}
	runtime := &jobRuntime{service: f.service, source: f.source, revision: child}
	// A check pipeline receives its own revision but retains its preview's trust
	// context. It must not resolve even its own protected credentials on denial.
	parentCtx := context.WithValue(ctx, previewTrustParentKey{}, f.previous)
	_, err := runtime.executeJob(parentCtx, f.resource, "qa", JobSpec{Run: "exit 99", Secrets: map[string]SecretBinding{"TOKEN": {SecretRef: "unavailable-secret"}}}, true)
	if !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatalf("check pipeline escaped parent trust: %v", err)
	}
	legacy := core.App{SourceRepo: "https://github.example/example/gitops", HelmProvenance: core.HelmProvenance{PullRequests: []core.HelmPullRequest{{Repository: "example/service", Number: 42, GitHubAppID: f.source.GitHubAppID, CommitSHA: head.Head.SHA}}}}
	if err := f.service.CheckDeploymentTrust(ctx, legacy, f.previous.Sources["gitops"].CommitSHA); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("legacy preview accepted fork code")
	}
}

func TestPreviewTrustTemplatePolicySurvivesDeletion(t *testing.T) {
	f, _ := sourceTrustFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	template := core.WorkflowPreviewTemplate{ID: "strict-template", ConfigSourceID: f.source.ID, GitHubAppID: f.source.GitHubAppID, Name: "Strict", SourceTrustPolicy: "approval_required", CreatedAt: now, UpdatedAt: now}
	if err := f.data.CreateWorkflowPreviewTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	triggers, err := f.data.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Link the pre-existing fixture trigger through the same persistent source update.
	trigger := triggers[0]
	trigger.TemplateID = template.ID
	// Creation exercises inheritance; close the old trigger before creating its successor.
	if err := f.data.CloseWorkflowPreviewTrigger(ctx, trigger.ID, now); err != nil {
		t.Fatal(err)
	}
	trigger.ID = "strict-trigger"
	trigger.Command = "/strict"
	trigger.ClosedAt = nil
	if err := f.data.CreateWorkflowPreviewTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.SourceTrust(ctx, f.resource, f.previous); !errors.Is(err, ErrPreviewSourceTrust) {
		t.Fatal("strict policy trusted same-repository code without approval")
	}
	if err := f.data.DeleteWorkflowPreviewTemplate(ctx, template.ID); err != nil {
		t.Fatal(err)
	}
	decision, err := f.service.SourceTrust(ctx, f.resource, f.previous)
	if !errors.Is(err, ErrPreviewSourceTrust) || decision.Policy != "approval_required" {
		t.Fatalf("deleting template weakened policy: %+v %v", decision, err)
	}
}

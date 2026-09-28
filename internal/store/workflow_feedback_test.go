package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestWorkflowFeedbackLeaseAndIndependentProgress(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "feedback.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	resource := "preview"
	for _, err := range []error{
		data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}),
		data.CreateSecret(ctx, core.Secret{ID: "credential", Name: "Credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture", CreatedAt: now}),
		data.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", CredentialSecretID: "credential", Name: "Source", Repository: "example/repo", CreatedAt: now, UpdatedAt: now}),
		data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: resource, ConfigSourceID: "source", Kind: "Application", Name: "preview", Temporary: true, CreatedAt: now, UpdatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := data.AcquireWorkflowFeedbackLease(ctx, resource, "controller-one", now, now.Add(time.Minute))
	if err != nil || !first {
		t.Fatalf("first lease: %v %v", first, err)
	}
	second, err := data.AcquireWorkflowFeedbackLease(ctx, resource, "controller-two", now, now.Add(time.Minute))
	if err != nil || second {
		t.Fatalf("two controllers acquired lease: %v %v", second, err)
	}
	if err := data.ReleaseWorkflowFeedbackLease(ctx, resource, "wrong-controller"); err != nil {
		t.Fatal(err)
	}
	second, err = data.AcquireWorkflowFeedbackLease(ctx, resource, "controller-two", now.Add(2*time.Minute), now.Add(3*time.Minute))
	if err != nil || !second {
		t.Fatalf("expired lease not recoverable: %v %v", second, err)
	}
	revision := core.WorkflowRevision{ID: "qa", ResourceID: resource, State: "running", Trigger: "pull request test 1", Feedback: &core.WorkflowFeedback{DeploymentID: "deployment", Targets: []core.WorkflowFeedbackTarget{{Status: "pending"}}}, CreatedAt: now}
	if err := data.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	progress := *revision.Feedback
	progress.Targets = []core.WorkflowFeedbackTarget{{Status: "success", Review: "approved", ReviewID: 99}}
	progress.Complete = true
	if err := data.UpdateWorkflowFeedback(ctx, revision.ID, &progress); err != nil {
		t.Fatal(err)
	}
	revision.State = "succeeded"
	if err := data.UpdateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetWorkflowRevision(ctx, revision.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Feedback.Targets[0].ReviewID != 99 || !stored.Feedback.Complete {
		t.Fatal("worker update overwrote reporting progress")
	}
	pending, err := data.PendingWorkflowFeedback(ctx, "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("completed report still pending: %+v %v", pending, err)
	}
}

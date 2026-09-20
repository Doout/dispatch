package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestWorkflowRecoveryPreservesApprovalsAndSuccessfulArtifacts(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "recovery.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(data.Migrate(ctx))
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	must(data.CreateProject(ctx, core.Project{ID: "recovery-project", Name: "recovery-project", CreatedAt: past}))
	must(data.CreateSecret(ctx, core.Secret{ID: "recovery-source-secret", Name: "recovery-source-secret", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture-encrypted-value", CreatedAt: time.Now().UTC()}))
	must(data.CreateConfigSource(ctx, core.ConfigSource{CredentialSecretID: "recovery-source-secret", ID: "recovery-source", ProjectID: "recovery-project", Name: "recovery-source", Repository: "example/repo", CreatedAt: past, UpdatedAt: past}))
	must(data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "recovery-resource", ConfigSourceID: "recovery-source", Name: "application", Kind: "Application", Path: "app.yaml", CreatedAt: past, UpdatedAt: past}))
	for _, state := range []string{"running", "awaiting_approval", "succeeded"} {
		id := "recovery-" + state
		must(data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: id, ResourceID: "recovery-resource", State: state, CreatedAt: past, Sources: map[string]core.WorkflowSourceRevision{}, Outputs: map[string]map[string]string{}}))
		must(data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: id + "-stage", RevisionID: id, StageName: "production", State: state, Approval: "required", CreatedAt: past, DeploymentIDs: []string{"retained-deployment"}}))
	}
	must(data.CreateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: "completed-build", ResourceID: "recovery-resource", RevisionID: "recovery-running", JobName: "build", State: "succeeded", Outputs: map[string]string{"image": "retained-digest"}, CreatedAt: past}))
	must(data.CreateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: "interrupted-build", ResourceID: "recovery-resource", RevisionID: "recovery-running", JobName: "publish", State: "running", CreatedAt: past}))
	must(data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "new-controller-work", ResourceID: "recovery-resource", State: "running", CreatedAt: now.Add(time.Second)}))
	count, err := data.RecoverInterruptedWorkflows(ctx, now)
	must(err)
	if count != 3 {
		t.Fatalf("recovered %d records, want 3", count)
	}
	for _, state := range []string{"awaiting_approval", "succeeded"} {
		revision, err := data.GetWorkflowRevision(ctx, "recovery-"+state)
		must(err)
		if revision.State != state {
			t.Fatal("recovery changed durable result or pending approval")
		}
	}
	revision, err := data.GetWorkflowRevision(ctx, "recovery-running")
	must(err)
	if revision.State != "failed" || revision.FinishedAt == nil {
		t.Fatal("interrupted revision remained active")
	}
	jobs, err := data.ListWorkflowJobResults(ctx, revision.ID)
	must(err)
	for _, job := range jobs {
		if job.ID == "completed-build" && (job.State != "succeeded" || job.Outputs["image"] != "retained-digest") {
			t.Fatal("recovery discarded reusable successful build")
		}
	}
	fresh, err := data.GetWorkflowRevision(ctx, "new-controller-work")
	must(err)
	if fresh.State != "running" {
		t.Fatal("startup recovery touched newer work")
	}
	count, err = data.RecoverInterruptedWorkflows(ctx, now)
	must(err)
	if count != 0 {
		t.Fatal("recovery was not idempotent")
	}
}

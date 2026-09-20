package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestApprovalRejectsChangedConfiguration(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "approval.db"))
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
	must(data.CreateProject(ctx, core.Project{ID: "approval-project", Name: "approval-project", CreatedAt: now}))
	must(data.CreateSecret(ctx, core.Secret{ID: "approval-source-secret", Name: "approval-source-secret", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture-encrypted-value", CreatedAt: time.Now().UTC()}))
	must(data.CreateConfigSource(ctx, core.ConfigSource{CredentialSecretID: "approval-source-secret", ID: "approval-source", ProjectID: "approval-project", Name: "approval-source", CreatedAt: now, UpdatedAt: now}))
	must(data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "approval-resource", ConfigSourceID: "approval-source", Name: "application", Kind: "Application", SpecDigest: "new-config", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	must(data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "approval-revision", ResourceID: "approval-resource", SpecDigest: "captured-config", State: "awaiting_approval", CreatedAt: now}))
	must(data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "approval-stage", RevisionID: "approval-revision", StageName: "production", Approval: "required", State: "awaiting_approval", CreatedAt: now}))
	s := NewService(data, nil, nil, nil, nil)
	if _, err = s.ApproveStage(ctx, "approval-stage"); err == nil || !strings.Contains(err.Error(), "configuration changed") {
		t.Fatalf("changed configuration approval result: %v", err)
	}
	stage, err := data.GetWorkflowStageRun(ctx, "approval-stage")
	must(err)
	if stage.State != "awaiting_approval" {
		t.Fatal("rejected approval changed the stage")
	}
}

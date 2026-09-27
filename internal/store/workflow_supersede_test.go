package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestSupersedeWorkflowRevisionsCancelsActiveWork(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "supersede.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, create := range []func() error{
		func() error {
			return data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now})
		},
		func() error {
			return data.CreateSecret(ctx, core.Secret{ID: "credential", Name: "Credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture", CreatedAt: now})
		},
		func() error {
			return data.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", CredentialSecretID: "credential", Name: "Source", Repository: "example/repo", CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "preview", ConfigSourceID: "source", Kind: "Application", Name: "preview", Temporary: true, CreatedAt: now, UpdatedAt: now})
		},
		func() error {
			return data.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "old", ResourceID: "preview", State: "running", Trigger: "pull request update", CreatedAt: now})
		},
		func() error {
			return data.CreateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: "job", ResourceID: "preview", RevisionID: "old", JobName: "build", State: "running", CreatedAt: now})
		},
		func() error {
			return data.CreateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "stage", RevisionID: "old", StageName: "dev", State: "running", CreatedAt: now})
		},
	} {
		if err := create(); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := data.SupersedeWorkflowRevisions(ctx, "preview")
	if err != nil || len(ids) != 1 || ids[0] != "old" {
		t.Fatalf("superseded revisions: %v, %v", ids, err)
	}
	revision, err := data.GetWorkflowRevision(ctx, "old")
	if err != nil || revision.State != "cancelled" || revision.FinishedAt == nil {
		t.Fatalf("revision was not cancelled: %+v, %v", revision, err)
	}
	jobs, err := data.ListWorkflowJobResults(ctx, "old")
	if err != nil || len(jobs) != 1 || jobs[0].State != "cancelled" {
		t.Fatalf("job was not cancelled: %+v, %v", jobs, err)
	}
	stage, err := data.GetWorkflowStageRun(ctx, "stage")
	if err != nil || stage.State != "cancelled" {
		t.Fatalf("stage was not cancelled: %+v, %v", stage, err)
	}
	if err := data.UpdateWorkflowRevision(ctx, core.WorkflowRevision{ID: "old", State: "succeeded"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late revision write revived cancelled run: %v", err)
	}
	if err := data.UpdateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: "job", State: "succeeded"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late job write revived cancelled run: %v", err)
	}
	if err := data.UpdateWorkflowStageRun(ctx, core.WorkflowStageRun{ID: "stage", State: "succeeded"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("late stage write revived cancelled run: %v", err)
	}
}

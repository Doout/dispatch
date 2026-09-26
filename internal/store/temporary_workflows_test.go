package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestRepositorySyncPreservesTemporaryWorkflow(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "temporary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := data.CreateProject(ctx, core.Project{ID: "project", Name: "project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateSecret(ctx, core.Secret{ID: "credential", Name: "credential", Type: core.SecretTypeText, Source: core.SecretSourceLocal, EnvironmentVariable: "TOKEN", EncryptedValue: "cipher", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{ID: "source", ProjectID: "project", CredentialSecretID: "credential", Name: "source", Repository: "git@example.test:owner/config.git", Branch: "main", Path: "deployment", SyncMode: core.ConfigSyncPoll, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	resource := core.WorkflowResource{ID: "temporary", ConfigSourceID: source.ID, APIVersion: "dispatch/v1alpha1", Kind: "Application", Name: "preview", Path: "temporary/temporary.yaml", Document: "inline", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if err := data.ReplaceWorkflowResources(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	got, err := data.GetWorkflowResource(ctx, resource.ID)
	if err != nil || !got.Temporary || !got.Active || got.State != "ready" || got.Document != "inline" {
		t.Fatalf("temporary resource changed during sync: %#v, %v", got, err)
	}
}

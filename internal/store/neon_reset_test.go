package store

import (
	"context"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNeonReplacementSQLite(t *testing.T) {
	testNeonReplacement(t, filepath.Join(t.TempDir(), "neon.db"))
}
func TestNeonReplacementPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testNeonReplacement(t, dsn)
}
func testNeonReplacement(t *testing.T, dsn string) {
	s := mutationStore(t, dsn)
	ctx := context.Background()
	id := ulid.Make().String()
	now := time.Now().UTC()
	project := core.Project{ID: id + "p", Name: id, CreatedAt: now}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	provider := core.NeonProvider{ID: id + "provider", ProjectID: project.ID, Name: "fixture", CreatedAt: now}
	if err := s.CreateNeonProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSecret(ctx, core.Secret{ID: id + "credential", Name: "neon-fixture-" + id, EnvironmentVariable: "NEON_FIXTURE_" + id, EncryptedValue: "cipher", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{CredentialSecretID: id + "credential", ID: id + "source", ProjectID: project.ID, Name: "fixture", Repository: "example/app", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	preview := core.WorkflowResource{ID: id + "preview", ConfigSourceID: source.ID, Kind: "Application", Name: "fixture", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	target := core.ServiceProvisionTarget{Provider: "neon", ProviderRef: provider.ID, ResourceName: "branch"}
	run := core.ServiceProvisionRun{ID: id + "run", ProjectID: project.ID, TemplateID: id + "template", ServiceName: "original", Target: &target, State: "queued", CreatedAt: now}
	record := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: id + "service", Name: run.ServiceName, Target: target, State: "accepted", Policy: "retain", Revision: 1, OperationID: run.ID, EncryptedRequest: "cipher", PreviewID: preview.ID, PreviewAlias: "db", CreatedAt: now, UpdatedAt: now}
	if err := s.CreateServiceResource(ctx, run, record); err != nil {
		t.Fatal(err)
	}
	old, err := s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "recovering", "old", now, "")
	if err != nil {
		t.Fatal(err)
	}
	old.State, old.ResourceID, old.EncryptedOutputs = "ready", "old-branch", "outputs"
	connection := core.Service{ID: old.ServiceID, ProjectID: project.ID, Name: old.Name, Type: "postgresql", ProvisionRunID: old.RunID, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = s.SaveServiceResource(ctx, old, run, &connection); err != nil {
		t.Fatal(err)
	}
	old, _ = s.GetServiceResource(ctx, old.RunID)
	preview.Active = false
	if err = s.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal(err)
	}
	candidate := record
	candidate.RunID = id + "replacement"
	candidate.ServiceID = id + "next-service"
	candidate.Name = "replacement"
	candidate.OperationID = candidate.RunID
	candidate.ReplacesRunID = old.RunID
	candidate.ReplacesRevision = old.Revision
	nextRun := run
	nextRun.ID = candidate.RunID
	nextRun.ServiceName = candidate.Name
	if err = s.CreateServiceResource(ctx, nextRun, candidate); err != nil {
		t.Fatal(err)
	}
	preview.Active = true
	if err = s.UpdateWorkflowResource(ctx, preview); err == nil {
		t.Fatal("resumed unresolved replacement")
	}
	if _, err = s.BeginWorkflowPreviewCleanup(ctx, preview.ID, "closed", now); err == nil {
		t.Fatal("cleanup ignored replacement")
	}
	claimed, err := s.ClaimServiceResource(ctx, candidate.RunID, 1, candidate.RunID, "recovering", "next", now, "")
	if err != nil {
		t.Fatal(err)
	}
	claimed.ProviderPhase = "Creating isolated Neon branch"
	claimed.ProviderCreateAttempted = true
	if err = s.CheckpointNeonResource(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	claimed.State = "unresolved"
	if err = s.SaveServiceResource(ctx, claimed, nextRun, nil); err != nil {
		t.Fatal(err)
	}
	linked, _ := s.GetNeonPreviewService(ctx, preview.ID, "db")
	if linked != old.RunID {
		t.Fatal("failure changed old binding")
	}
	latest, _ := s.GetServiceResource(ctx, candidate.RunID)
	claimed, err = s.ClaimServiceResource(ctx, latest.RunID, latest.Revision, latest.RunID, "recovering", "recovered", now, "")
	if err != nil {
		t.Fatal(err)
	}
	claimed.State, claimed.ResourceID, claimed.EncryptedOutputs = "ready", "new-branch", "encrypted-new-output"
	connection.ID, connection.ProvisionRunID, connection.Name = candidate.ServiceID, candidate.RunID, candidate.Name
	if err = s.SaveServiceResource(ctx, claimed, nextRun, &connection); err != nil {
		t.Fatal(err)
	}
	linked, _ = s.GetNeonPreviewService(ctx, preview.ID, "db")
	if linked != candidate.RunID {
		t.Fatal("new binding not committed")
	}
	if _, err = s.GetService(ctx, old.ServiceID); err != nil {
		t.Fatal("old connection removed")
	}
	if err = s.UpdateWorkflowResource(ctx, preview); err != nil {
		t.Fatal("completed replacement kept preview blocked", err)
	}
	if err = s.DeleteUnusedNeonProvider(ctx, provider.ID); err == nil {
		t.Fatal("recovery provider removed")
	}
}

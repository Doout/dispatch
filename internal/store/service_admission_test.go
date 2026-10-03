package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestScopedServiceAdmissionSQLite(t *testing.T) {
	testScopedServiceAdmission(t, filepath.Join(t.TempDir(), "admission.db"))
}
func TestScopedServiceAdmissionPostgres(t *testing.T) {
	url := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testScopedServiceAdmission(t, url)
}
func testScopedServiceAdmission(t *testing.T, url string) {
	s := mutationStore(t, url)
	ctx := context.Background()
	id := ulid.Make().String()
	now := time.Now().UTC()
	project := core.Project{ID: id + "p", Name: id, CreatedAt: now}
	server := core.Server{ID: id + "s", Name: id, Runtime: "docker", Address: "local", CreatedAt: now}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	template, digest := id+"template", "reviewed-digest"
	if err := s.SaveInfrastructureAssignment(ctx, core.InfrastructureAssignment{ProjectID: project.ID, Kind: "service_template", ResourceID: template + "@" + digest, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	saved := core.SavedServiceTemplate{ID: template, ProjectID: project.ID, Name: "approved-template", Document: "approved-template-document", Digest: digest, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateSavedServiceTemplate(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInfrastructureAssignment(ctx, core.InfrastructureAssignment{ProjectID: project.ID, Kind: "target", ResourceID: server.ID, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	scoped := core.WithScopedServiceAdmission(ctx, core.ScopedServiceAdmission{TemplateID: template, Digest: digest})
	create := func(ctx context.Context, suffix string) error {
		run := core.ServiceProvisionRun{ID: id + suffix, TemplateID: template, ProjectID: project.ID, ServiceName: "service-" + suffix, State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: "owned-" + suffix}, CreatedAt: now}
		record := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: id + suffix + "binding", Name: run.ServiceName, Target: *run.Target, State: "accepted", Policy: "retain", Revision: 1, OperationID: run.ID, CreatedAt: now, UpdatedAt: now, EncryptedRequest: "cipher"}
		return s.CreateServiceResource(ctx, run, record)
	}
	var violation *core.InfrastructureQuotaViolation
	if err := create(scoped, "no-policy"); !errors.As(err, &violation) || violation.Code != "policy_required" {
		t.Fatal("missing policy admitted", err)
	}
	policy := core.InfrastructureQuotaPolicy{ProjectID: project.ID, Revision: 1, MaxServices: 1, Providers: []core.InfrastructureProviderRule{}, UpdatedAt: now}
	if err := s.SaveInfrastructureQuotaPolicy(ctx, policy, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results <- create(scoped, fmt.Sprintf("concurrent-%d", i)) }(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
			continue
		}
		violation = nil
		if !errors.As(err, &violation) || violation.Limit != "maxServices" {
			t.Fatal("unexpected admission error", err)
		}
	}
	if admitted != 1 {
		t.Fatal("concurrent admissions escaped limit", admitted)
	}
	count, err := s.CountServiceResources(ctx, project.ID)
	if err != nil || count != 1 {
		t.Fatal("incorrect durable usage", count, err)
	}
	// Uncertain and failed outcomes remain owned and counted, independent of run success.
	_, err = s.db.ExecContext(ctx, s.q(`UPDATE service_resources SET state='unresolved' WHERE project_id=?`), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	violation = nil
	if err = create(scoped, "uncertain"); !errors.As(err, &violation) {
		t.Fatal("uncertain allocation released capacity", err)
	}
	// Revoking exact template approval blocks acceptance even with unlimited policy.
	policy.Revision++
	policy.MaxServices = -1
	if err = s.SaveInfrastructureQuotaPolicy(ctx, policy, 1); err != nil {
		t.Fatal(err)
	}
	// Model a reviewed API preflight followed by a concurrent template edit.
	saved.Revision++
	saved.Digest = "edited-digest"
	if err = s.UpdateSavedServiceTemplate(ctx, saved, 1); err != nil {
		t.Fatal(err)
	}
	if err = create(scoped, "stale-template"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("stale definition accepted", err)
	}
	saved.Revision++
	saved.Digest = digest
	if err = s.UpdateSavedServiceTemplate(ctx, saved, 2); err != nil {
		t.Fatal(err)
	}
	// A target revoked after preflight must also fail at the transaction boundary.
	if err = s.DeleteInfrastructureAssignment(ctx, project.ID, "target", server.ID); err != nil {
		t.Fatal(err)
	}
	if err = create(scoped, "revoked-target"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("revoked target accepted", err)
	}
	if err = s.SaveInfrastructureAssignment(ctx, core.InfrastructureAssignment{ProjectID: project.ID, Kind: "target", ResourceID: server.ID, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Repository-backed definitions use the same current-digest and active-source guard.
	previousTemplate := template
	template = id + "repo-template"
	if err = s.CreateSecret(ctx, core.Secret{ID: id + "source-credential", Name: id + "credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{CredentialSecretID: id + "source-credential", ID: id + "config", ProjectID: project.ID, Name: "repository", Active: true, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	resource := core.WorkflowResource{ID: template, ConfigSourceID: source.ID, Kind: "ServiceTemplate", Name: "repository-template", Active: true, ConfigSHA: digest, Document: "reviewed-document", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveInfrastructureAssignment(ctx, core.InfrastructureAssignment{ProjectID: project.ID, Kind: "service_template", ResourceID: template + "@" + digest, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	repoScope := core.WithScopedServiceAdmission(ctx, core.ScopedServiceAdmission{TemplateID: template, Digest: digest})
	resource.ConfigSHA = "changed-repository-revision"
	if err = s.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if err = create(repoScope, "stale-repository"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("stale repository accepted", err)
	}
	resource.ConfigSHA = digest
	resource.Active = false
	if err = s.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	if err = create(repoScope, "inactive-repository"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("inactive repository accepted", err)
	}
	resource.Active = true
	if err = s.UpdateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	source.Active = false
	if err = s.UpdateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err = create(repoScope, "inactive-source"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("disabled source accepted", err)
	}
	template = previousTemplate
	if err = s.DeleteInfrastructureAssignment(ctx, project.ID, "service_template", template+"@"+digest); err != nil {
		t.Fatal(err)
	}
	if err = create(scoped, "revoked"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("revoked template accepted", err)
	}
}

func TestServiceAssignmentRevocationWaitsForAcceptedTransactionPostgres(t *testing.T) {
	url := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	s := mutationStore(t, url)
	ctx := context.Background()
	project := core.Project{ID: ulid.Make().String(), Name: "Lock boundary", CreatedAt: time.Now().UTC()}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"target", "service_template"} {
		id := ulid.Make().String()
		if err := s.SaveInfrastructureAssignment(ctx, core.InfrastructureAssignment{ProjectID: project.ID, Kind: kind, ResourceID: id, UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.lockServiceAssignment(ctx, tx.Tx, project.ID, kind, id); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() { result <- s.DeleteInfrastructureAssignment(ctx, project.ID, kind, id) }()
		select {
		case err := <-result:
			tx.Rollback()
			t.Fatalf("%s revocation passed active acceptance: %v", kind, err)
		case <-time.After(150 * time.Millisecond):
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("revocation did not resume after acceptance")
		}
	}
}

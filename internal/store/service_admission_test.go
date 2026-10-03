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
	if err = s.DeleteInfrastructureAssignment(ctx, project.ID, "service_template", template+"@"+digest); err != nil {
		t.Fatal(err)
	}
	if err = create(scoped, "revoked"); !errors.Is(err, ErrServiceResourceChanged) {
		t.Fatal("revoked template accepted", err)
	}
}

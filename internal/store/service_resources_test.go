package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestServiceResourceSQLite(t *testing.T) {
	testServiceResourceStore(t, filepath.Join(t.TempDir(), "resources.db"))
}
func TestServiceResourcePostgres(t *testing.T) {
	url := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testServiceResourceStore(t, url)
}
func testServiceResourceStore(t *testing.T, url string) {
	ctx := context.Background()
	s := mutationStore(t, url)
	prefix := ulid.Make().String()
	now := time.Now().UTC()
	project := core.Project{ID: prefix + "p", Name: prefix, CreatedAt: now}
	server := core.Server{ID: prefix + "s", Name: prefix, Runtime: "docker", Address: "local", CreatedAt: now}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	run := core.ServiceProvisionRun{ID: prefix + "r", TemplateID: prefix + "t", ProjectID: project.ID, ServiceName: prefix, State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: server.ID, ResourceName: "owned-resource"}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: project.ID, ServiceID: prefix + "binding", Name: run.ServiceName, Target: *run.Target, State: "accepted", Policy: "retain", Revision: 1, OperationID: run.ID, CreatedAt: now, UpdatedAt: now, EncryptedRequest: "encrypted-request-fixture"}
	if err := s.CreateServiceResource(ctx, run, resource); err != nil {
		t.Fatal(err)
	}
	claim, err := s.ClaimServiceResource(ctx, run.ID, 1, run.ID, "provisioning", "old-controller", now, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	recovered, err := s.ClaimServiceResource(ctx, run.ID, claim.Revision, run.ID, "recovering", "new-controller", now.Add(32*time.Minute), "")
	if err != nil {
		t.Fatal(err)
	}
	claim.State = "ready"
	if err = s.SaveServiceResource(ctx, claim, run, nil); err == nil {
		t.Fatal("stale controller changed resource after restart")
	}
	recovered.State, recovered.ResourceID, recovered.EncryptedOutputs = "ready", "original-runtime-id", "encrypted-output-fixture"
	run.State, run.ServiceID = "succeeded", resource.ServiceID
	service := core.Service{ID: resource.ServiceID, ProjectID: project.ID, Name: run.ServiceName, Type: "postgresql", ProvisionRunID: run.ID, ProvisionTarget: run.Target, Fields: map[string]core.ServiceField{"connectionUrl": {Sensitive: true, Configured: true, EncryptedValue: "encrypted-connection"}}, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err = s.SaveServiceResource(ctx, recovered, run, &service); err != nil {
		t.Fatal(err)
	}
	saved, err := s.GetServiceResource(ctx, run.ID)
	if err != nil || saved.EncryptedRequest != resource.EncryptedRequest || saved.ResourceID != "original-runtime-id" {
		t.Fatal("restart lost recovery identity", err)
	}
	app := core.App{ID: prefix + "a", ProjectID: project.ID, ServerID: server.ID, Name: "Consumer", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	if err = s.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	bindings := []core.ServiceBinding{{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, bindings); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimServiceResource(ctx, run.ID, saved.Revision, "delete", "deleting", "cleanup", now, "original-runtime-id"); !errors.Is(err, ErrServiceInUse) {
		t.Fatal("in-use deletion accepted", err)
	}
	deployment := core.Deployment{ID: prefix + "d", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err = s.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if busy, err := s.ServiceResourceConsumers(ctx, service.ID); err != nil || !busy {
		t.Fatal("detached configuration hid live deployed consumer", err)
	}
	deployment.ID = prefix + "d2"
	deployment.CreatedAt = now.Add(time.Second)
	if err = s.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	deletion, err := s.ClaimServiceResource(ctx, run.ID, saved.Revision, "delete", "deleting", "cleanup", now, "original-runtime-id")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, bindings); err == nil {
		t.Fatal("late consumer bypassed deletion admission")
	}
	deletion.State = "deleted"
	if err = s.SaveServiceResource(ctx, deletion, run, nil); err != nil {
		t.Fatal(err)
	}
	// Retained rollback inputs cannot reactivate a connection to a removed workload.
	original, err := s.GetDeployment(ctx, prefix+"d")
	if err != nil {
		t.Fatal(err)
	}
	rollback := original
	rollback.ID, rollback.State, rollback.CreatedAt = prefix+"rollback", core.DeploymentQueued, now.Add(2*time.Second)
	if err = s.CreateRollbackDeployment(ctx, rollback, original, core.ReleaseAction{ID: prefix + "action", ProjectID: project.ID, AppID: app.ID, DeploymentID: rollback.ID, SourceDeploymentID: original.ID, CreatedAt: now}); err == nil {
		t.Fatal("retained rollback reactivated a deleted service resource")
	}
	if _, err = s.GetDeployment(ctx, rollback.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed rollback acceptance left a deployment", err)
	}
	if _, err = s.GetService(ctx, service.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("removed resource retained live connection")
	}
	saved, err = s.GetServiceResource(ctx, run.ID)
	if err != nil || saved.EncryptedOutputs == "" || saved.State != "deleted" {
		t.Fatal("cleanup discarded protected history", err)
	}
}

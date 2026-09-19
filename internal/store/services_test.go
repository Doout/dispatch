package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestServicePersistenceAndDeploymentIsolation(t *testing.T) {
	testServicePersistence(t, filepath.Join(t.TempDir(), "services.db"))
}
func TestServicePostgresPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_SERVICES_STORE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_SERVICES_STORE_POSTGRES_URL to a disposable empty database")
	}
	testServicePersistence(t, dsn)
}
func testServicePersistence(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"svc-project", "other-project"} {
		if err = s.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.CreateServer(ctx, core.Server{ID: "svc-server", Name: "local", Address: "local", Runtime: "docker", State: "ready", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "svc-app", Name: "service-app", ProjectID: "svc-project", ServerID: "svc-server", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	if err = s.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	service := core.Service{ID: "svc-db", Name: "database", ProjectID: app.ProjectID, Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"url": {Sensitive: true, Configured: true, EncryptedValue: "encrypted-original"}}, CreatedAt: now, UpdatedAt: now}
	if err = s.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	bindings := []core.ServiceBinding{{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "url"}}}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, bindings); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteService(ctx, service.ID); !errors.Is(err, ErrServiceInUse) {
		t.Fatalf("bound service deletion: %v", err)
	}
	deployment := core.Deployment{ID: "svc-run", AppID: app.ID, State: core.DeploymentQueued, CreatedAt: now}
	if err = s.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	service.Fields["url"] = core.ServiceField{Sensitive: true, Configured: true, EncryptedValue: "encrypted-new"}
	service.Revision = 2
	if err = s.UpdateService(ctx, service, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.UpdateService(ctx, service, 1); !errors.Is(err, ErrServiceConflict) {
		t.Fatalf("stale update accepted: %v", err)
	}
	captured, err := s.GetDeploymentServiceBindings(ctx, deployment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || captured[0].Service.Revision != 1 || captured[0].Service.Fields["url"].EncryptedValue != "encrypted-original" {
		t.Fatalf("snapshot changed: %#v", captured)
	}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteService(ctx, service.ID); !errors.Is(err, ErrServiceInUse) {
		t.Fatalf("active deployment did not protect service: %v", err)
	}
	deployment.State = core.DeploymentSucceeded
	if err = s.UpdateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, bindings); err != nil {
		t.Fatal(err)
	}
	consumers, err := s.ListServiceConsumers(ctx, service.ID)
	if err != nil || len(consumers) != 1 || consumers[0].AppliedRevision != 1 || !consumers[0].RedeploymentRequired {
		t.Fatalf("incorrect consumers: %#v %v", consumers, err)
	}
	other := service
	other.ID = "svc-foreign"
	other.ProjectID = "other-project"
	if err = s.CreateService(ctx, other); err != nil {
		t.Fatal(err)
	}
	bindings[0].ServiceRef = other.ID
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, bindings); err == nil {
		t.Fatal("accepted cross-project service")
	}
	if err = s.ReplaceAppServiceBindings(ctx, app.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteService(ctx, service.ID); err != nil {
		t.Fatal(err)
	}
	captured, err = s.GetDeploymentServiceBindings(ctx, deployment.ID)
	if err != nil || len(captured) != 1 {
		t.Fatal("service deletion removed historical evidence", err)
	}
}

func TestGlobalSecretRotationPreservesCapturedCipherAndMarksConsumers(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "references.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now})
	s.CreateServer(ctx, core.Server{ID: "s", Name: "s", Runtime: "docker", CreatedAt: now})
	s.CreateApp(ctx, core.App{ID: "app", ProjectID: "p", ServerID: "s", Name: "app", BuildType: core.BuildTypeDockerfile, CreatedAt: now})
	secret := core.Secret{ID: "global", Name: "global", Type: core.SecretTypeText, Source: core.SecretSourceLocal, EnvironmentVariable: "GLOBAL", EncryptedValue: "original-cipher", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	connection := core.Service{ID: "db", ProjectID: "p", Name: "db", Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"password": {Sensitive: true, Configured: true, SecretRef: secret.ID}}}
	if err = s.CreateService(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err = s.ReplaceAppServiceBindings(ctx, "app", []core.ServiceBinding{{Alias: "db", ServiceRef: "db", Environment: map[string]string{"PASSWORD": "password"}}}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateDeployment(ctx, core.Deployment{ID: "run", AppID: "app", State: core.DeploymentQueued, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	secret.EncryptedValue = "rotated-cipher"
	if err = s.UpdateSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	captured, err := s.GetDeploymentServiceBindings(ctx, "run")
	if err != nil {
		t.Fatal(err)
	}
	field := captured[0].Service.Fields["password"]
	if field.CapturedSecretID != "global" || field.CapturedSecretValue != "original-cipher" {
		t.Fatal("referenced local credential was not captured")
	}
	current, err := s.GetService(ctx, "db")
	if err != nil || current.Revision != 2 {
		t.Fatal("global rotation did not revise service", current.Revision, err)
	}
}

func TestInvalidWorkflowRetainsServiceDependencies(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-services.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = s.CreateProject(ctx, core.Project{ID: "project", Name: "project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateSecret(ctx, core.Secret{ID: "source-auth", Name: "source-auth", Type: core.SecretTypeText, Source: core.SecretSourceLocal, EnvironmentVariable: "TOKEN", EncryptedValue: "cipher", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{ID: "source", CredentialSecretID: "source-auth", ProjectID: "project", Name: "source", Repository: "owner/config", Branch: "main", Path: ".dispatch", SyncMode: core.ConfigSyncPoll, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err = s.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	connection := core.Service{ID: "service", ProjectID: "project", Name: "service", Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{}}
	if err = s.CreateService(ctx, connection); err != nil {
		t.Fatal(err)
	}
	resource := core.WorkflowResource{ID: "resource", ConfigSourceID: source.ID, Kind: "Application", Name: "app", State: "ready", CreatedAt: now, UpdatedAt: now, ServiceIDs: []string{connection.ID}}
	if err = s.ReplaceWorkflowResources(ctx, source, []core.WorkflowResource{resource}); err != nil {
		t.Fatal(err)
	}
	// Reloaded invalid resources carry their accepted document, not computed IDs.
	resource.State, resource.ServiceIDs = "invalid", nil
	if err = s.ReplaceWorkflowResources(ctx, source, []core.WorkflowResource{resource}); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteService(ctx, connection.ID); !errors.Is(err, ErrServiceInUse) {
		t.Fatalf("invalid resource lost deletion protection: %v", err)
	}
	if err = s.ReplaceWorkflowResources(ctx, source, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteService(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
}

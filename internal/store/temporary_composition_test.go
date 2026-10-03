package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/routing"
	"github.com/oklog/ulid/v2"
)

func TestTemporaryCompositionAdmissionSQLite(t *testing.T) {
	testTemporaryCompositionAdmission(t, filepath.Join(t.TempDir(), "composition.db"))
}
func TestTemporaryCompositionAdmissionPostgres(t *testing.T) {
	url := isolatedPostgresURL(t, "DISPATCH_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL")
	}
	testTemporaryCompositionAdmission(t, url)
}
func testTemporaryCompositionAdmission(t *testing.T, url string) {
	s := mutationStore(t, url)
	ctx := context.Background()
	now := time.Now().UTC()
	project := core.Project{ID: "composition-project", Name: "Composition", CreatedAt: now}
	if err := s.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "composition-node", Name: "Node", Driver: "dispatch_agent", Config: map[string]string{}, Details: map[string]string{}, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "composition-node", EnrollmentHash: "hash", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollEdgeCredential(ctx, "composition-node", "hash", "public", "session", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	server := core.Server{ID: "composition-server", Name: "Target", Runtime: core.ServerRuntimeDocker, State: "ready", AgentNodeID: "composition-node", AgentMode: "outbound-runtime", Routing: &core.RoutingConfig{BaseDomain: "environments.example.test", EntryPoint: "web"}, CreatedAt: now}
	if err := s.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	template := core.App{ID: "composition-template", ProjectID: project.ID, ServerID: server.ID, Name: "Template", SourceRepo: "https://github.com/example/environment", Branch: "main", BuildType: core.BuildTypeDockerfile, DockerfilePath: "Dockerfile", Template: true, ContainerPort: 8080, CreatedAt: now}
	if err := s.CreateApp(ctx, template); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveInfrastructureQuotaPolicy(ctx, core.InfrastructureQuotaPolicy{ProjectID: project.ID, Revision: 1, MaxTemporaryEnvironments: 3, MaxTemporaryLifetimeSeconds: 7200, UpdatedAt: now}, 0); err != nil {
		t.Fatal(err)
	}
	service := core.Service{ID: "composition-service", ProjectID: project.ID, Name: "Shared", Type: "postgresql", Revision: 1, Fields: map[string]core.ServiceField{"connectionUrl": {Value: "postgresql://fixture.invalid/db", Configured: true}}, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateService(ctx, service); err != nil {
		t.Fatal(err)
	}
	binding := core.ServiceBinding{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}
	prepare := func() core.TemporaryEnvironmentReview {
		id := ulid.Make().String()
		clone := template
		clone.ID = id + "app"
		clone.Name = "composition-" + strings.ToLower(id[:8])
		clone.Template = false
		clone.Generated = true
		clone.Branch = strings.Repeat("a", 40)
		plan, err := routing.Plan(core.Deployment{}, clone, server)
		if err != nil {
			t.Fatal(err)
		}
		clone.Domain = plan.Hostname
		review := core.TemporaryEnvironmentReview{ID: id, EnvironmentID: id + "environment", Input: core.TemporaryEnvironmentInput{ProjectID: project.ID, TemplateID: template.ID, ServerID: server.ID, Name: clone.Name, SourceSHA: clone.Branch, LifetimeSeconds: 3600, ServiceBindings: []core.ServiceBinding{binding}}, TemplateDigest: template.SpecDigest(), Clone: clone, TargetNodeID: server.AgentNodeID, TargetGeneration: 1, Routing: server.Routing, Route: plan, ServiceRevisions: map[string]int64{service.ID: service.Revision}, Digest: id + "digest", State: "prepared", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
		if err = s.CreateTemporaryEnvironmentReview(ctx, review); err != nil {
			t.Fatal(err)
		}
		return review
	}
	stale := prepare()
	service.Revision++
	if err := s.UpdateService(ctx, service, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptTemporaryEnvironment(ctx, stale, core.Identity{ID: "owner"}, "stale-deployment", now); !errors.Is(err, ErrDeploymentReviewChanged) {
		t.Fatal("stale service revision accepted", err)
	}
	if _, err := s.GetApp(ctx, stale.Clone.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("rejected review left app", err)
	}
	review := prepare()
	e, err := s.AcceptTemporaryEnvironment(ctx, review, core.Identity{ID: "owner"}, "composition-deployment", now)
	if err != nil {
		t.Fatal(err)
	}
	route, err := s.GetApplicationRoute(ctx, e.AppID)
	if err != nil || route.Hostname != review.Route.Hostname || route.RequestedDeploymentID != e.DeploymentID {
		t.Fatal("hostname not reserved", route, err)
	}
	captures, err := s.GetDeploymentServiceBindings(ctx, e.DeploymentID)
	if err != nil || len(captures) != 1 || captures[0].Service.Revision != service.Revision {
		t.Fatal("bindings not captured", captures, err)
	}
	d, err := s.GetDeployment(ctx, e.DeploymentID)
	if err != nil || d.SpecDigest == review.Clone.SpecDigest() || e.AppSpecDigest != review.Clone.SpecDigest() {
		t.Fatal("application/bound digest ownership invalid", d, e, err)
	}
	found := false
	for _, resource := range e.Resources {
		if resource.Kind == "service" && resource.ID == service.ID && resource.Ownership == "shared" {
			found = true
		}
	}
	if !found {
		t.Fatal("shared service not tracked", e.Resources)
	}
	changed := prepare()
	server.Routing = &core.RoutingConfig{BaseDomain: "changed.example.test", EntryPoint: "web"}
	if err = s.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AcceptTemporaryEnvironment(ctx, changed, core.Identity{ID: "owner"}, "changed-route-deployment", now); !errors.Is(err, ErrTemporaryEnvironmentChanged) {
		t.Fatal("changed routing accepted", err)
	}
}

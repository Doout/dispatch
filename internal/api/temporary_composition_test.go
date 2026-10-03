package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/remoteruntime"
	"github.com/doout/dispatch/internal/store"
)

func temporarySharedService(t *testing.T, a *API, project string) core.Service {
	t.Helper()
	var service core.Service
	raw := serviceRequestTest(t, a, "POST", "/api/v1/services", map[string]any{"projectId": project, "name": "environment-db", "type": "postgresql", "connectionUrl": "postgresql://preview:fixture-password@database.test/preview"}, 201)
	if err := json.Unmarshal(raw, &service); err != nil {
		t.Fatal(err)
	}
	return service
}
func temporaryCompositionReview(t *testing.T, a *API, app core.App, name string, bindings []core.ServiceBinding) core.TemporaryEnvironmentReview {
	t.Helper()
	input := core.TemporaryEnvironmentInput{ProjectID: app.ProjectID, TemplateID: app.ID, ServerID: app.ServerID, Name: name, SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 3600, ServiceBindings: bindings}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", input, 201)
	var review core.TemporaryEnvironmentReview
	if err := json.Unmarshal(raw, &review); err != nil {
		t.Fatal(err)
	}
	return review
}
func TestTemporaryEnvironmentExplicitSharedBindingsAreCapturedAndPinned(t *testing.T) {
	a, app := temporaryFixture(t)
	service := temporarySharedService(t, a, app.ProjectID)
	binding := core.ServiceBinding{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}
	if err := a.store.ReplaceAppServiceBindings(context.Background(), app.ID, []core.ServiceBinding{binding}); err != nil {
		t.Fatal(err)
	}
	omitted := temporaryCompositionReview(t, a, app, "no-inherited-data", nil)
	if len(omitted.Input.ServiceBindings) != 0 || len(omitted.ServiceRevisions) != 0 {
		t.Fatal("template service bindings were copied")
	}
	review := temporaryCompositionReview(t, a, app, "bound-investigation", []core.ServiceBinding{binding})
	if review.ServiceRevisions[service.ID] != service.Revision {
		t.Fatal("service revision was not reviewed")
	}
	w := temporaryKeyed(a, "/api/v1/temporary-environments", "create-bound-environment", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	receipt := decodeMutation(t, w)
	e, err := a.store.(*store.SQLStore).GetTemporaryEnvironment(context.Background(), review.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range e.Resources {
		if r.Kind == "service" && r.ID == service.ID {
			found = r.Ownership == "shared"
		}
	}
	if !found {
		t.Fatal("selected service not recorded as shared", e.Resources)
	}
	captured, err := a.store.GetDeploymentServiceBindings(context.Background(), receipt.OperationID)
	if err != nil || len(captured) != 1 || captured[0].Service.Revision != service.Revision {
		t.Fatal("binding was not frozen", captured, err)
	}
	clone, err := a.store.GetApp(context.Background(), e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.store.GetDeployment(context.Background(), e.DeploymentID)
	if err != nil || d.SpecDigest == clone.SpecDigest() {
		t.Fatal("service revisions missing from deployment digest", err)
	}
	if err = a.checkTemporaryExecution(context.Background(), clone, e.SourceSHA); err != nil {
		t.Fatal("bound deployment rejected during dispatch", err)
	}
	// Credential rotation after review must reject new acceptance, while an accepted capture stays frozen.
	stale := temporaryCompositionReview(t, a, app, "stale-service-review", []core.ServiceBinding{binding})
	serviceRequestTest(t, a, "PUT", "/api/v1/services/"+service.ID, map[string]any{"revision": service.Revision, "projectId": app.ProjectID, "name": service.Name, "type": "postgresql", "connectionUrl": "postgresql://preview:new-fixture-password@database.test/preview"}, 200)
	w = temporaryKeyed(a, "/api/v1/temporary-environments", "reject-stale-service", temporaryAcceptance{ReviewID: stale.ID, Digest: stale.Digest, ConfirmName: stale.Input.Name})
	if w.Code != 409 {
		t.Fatal("rotated service was accepted", w.Code, w.Body.String())
	}
	if err = a.checkTemporaryExecution(context.Background(), clone, e.SourceSHA); err != nil {
		t.Fatal("rotation changed already captured execution", err)
	}
}
func TestTemporaryEnvironmentManagedHostnameIsReviewedReservedAndConfigPinned(t *testing.T) {
	a, app := temporaryFixture(t)
	app.ContainerPort = 8080
	if err := a.store.UpdateApp(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	server, err := a.store.GetServer(context.Background(), app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.Routing = &core.RoutingConfig{BaseDomain: "sandbox.example.test", EntryPoint: "websecure", TLSResolver: "letsencrypt", RequireTLS: true}
	if err = a.store.UpdateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	first := temporaryCompositionReview(t, a, app, "isolated-route", nil)
	second := temporaryCompositionReview(t, a, app, "isolated-route", nil)
	if first.Route == nil || first.Clone.Domain != first.Route.Hostname || first.Clone.Domain == app.Domain || !strings.HasSuffix(first.Clone.Domain, ".sandbox.example.test") || first.Clone.Domain == second.Clone.Domain {
		t.Fatal("hostname was not uniquely reviewed", first, second)
	}
	if first.Routing == nil || !first.Routing.RequireTLS {
		t.Fatal("target routing not reviewed")
	}
	w := temporaryKeyed(a, "/api/v1/temporary-environments", "create-isolated-route", temporaryAcceptance{ReviewID: first.ID, Digest: first.Digest, ConfirmName: first.Input.Name})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	e, err := a.store.(*store.SQLStore).GetTemporaryEnvironment(context.Background(), first.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	route, err := a.store.(store.ApplicationRouteStore).GetApplicationRoute(context.Background(), e.AppID)
	if err != nil || route.Hostname != first.Route.Hostname || route.RequestedDeploymentID != e.DeploymentID || e.Hostname != route.Hostname {
		t.Fatal("route was not atomically owned", route, e, err)
	}
	// Direct store edit models a target config change during the API preflight window.
	server.Routing.BaseDomain = "changed.example.test"
	if err = a.store.UpdateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	w = temporaryKeyed(a, "/api/v1/temporary-environments", "reject-changed-routing", temporaryAcceptance{ReviewID: second.ID, Digest: second.Digest, ConfirmName: second.Input.Name})
	if w.Code != 409 {
		t.Fatal("changed route config accepted", w.Code, w.Body.String())
	}
}
func TestTemporaryEnvironmentRejectsCrossProjectBinding(t *testing.T) {
	a, app := temporaryFixture(t)
	other := core.Project{ID: "other-binding-project", Name: "Other", CreatedAt: time.Now().UTC()}
	if err := a.store.CreateProject(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	service := temporarySharedService(t, a, other.ID)
	input := core.TemporaryEnvironmentInput{ProjectID: app.ProjectID, TemplateID: app.ID, ServerID: app.ServerID, Name: "foreign-binding", SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 3600, ServiceBindings: []core.ServiceBinding{{Alias: "foreign", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}}
	serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", input, 422)
}

func TestTemporaryEnvironmentCleanupRemovesOwnedRouteAndRetainsSharedService(t *testing.T) {
	a, app := temporaryFixture(t)
	ctx := context.Background()
	app.ContainerPort = 8080
	if err := a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.Routing = &core.RoutingConfig{BaseDomain: "environments.example.test", EntryPoint: "web"}
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	service := temporarySharedService(t, a, app.ProjectID)
	review := temporaryCompositionReview(t, a, app, "retained-data-cleanup", []core.ServiceBinding{{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}})
	w := temporaryKeyed(a, "/api/v1/temporary-environments", "owned-route-create", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	data := a.store.(*store.SQLStore)
	e, err := data.GetTemporaryEnvironment(ctx, review.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	var cleanup temporaryCleanupReview
	raw := serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/"+e.ID+"/cleanup-review", nil, 200)
	if err = json.Unmarshal(raw, &cleanup); err != nil {
		t.Fatal(err)
	}
	w = temporaryKeyed(a, "/api/v1/temporary-environments/"+e.ID+"/destroy", "owned-route-cleanup", map[string]any{"revision": cleanup.Revision, "digest": cleanup.Digest, "confirmName": e.Name})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	done := make(chan struct{})
	go func() { defer close(done); a.reconcileTemporaryEnvironments(ctx) }()
	broker := a.runtimeBroker()
	var lease *remoteruntime.LeasedJob
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		lease, err = broker.Lease(ctx, server.AgentNodeID)
		if err != nil {
			t.Fatal(err)
		}
		if lease != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lease == nil || lease.Request.Operation != "destroy" || lease.Request.Application.ID != e.AppID {
		t.Fatal("owned cleanup not dispatched", lease)
	}
	if err = broker.Complete(ctx, server.AgentNodeID, lease.ID, remoteruntime.Completion{LeaseToken: lease.LeaseToken, Result: remoteruntime.Result{State: "succeeded"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not settle")
	}
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "closed" {
		t.Fatal("cleanup did not close ownership", e, err)
	}
	if _, err = data.GetApplicationRoute(ctx, e.AppID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("owned route survived cleanup", err)
	}
	kept, err := data.GetService(ctx, service.ID)
	if err != nil || kept.Revision != service.Revision {
		t.Fatal("shared service changed", kept, err)
	}
	bindings, err := data.GetAppServiceBindings(ctx, e.AppID)
	if err != nil || len(bindings) != 0 {
		t.Fatal("stopped workload still consumes shared service", bindings, err)
	}
	frozen, err := data.GetDeploymentServiceBindings(ctx, e.DeploymentID)
	if err != nil || len(frozen) != 1 {
		t.Fatal("cleanup erased execution history", frozen, err)
	}
}

func TestTemporaryEnvironmentHostnameConflictRollsBackAcceptance(t *testing.T) {
	a, app := temporaryFixture(t)
	ctx := context.Background()
	app.ContainerPort = 8080
	if err := a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.Routing = &core.RoutingConfig{BaseDomain: "collision.example.test", EntryPoint: "web"}
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	review := temporaryCompositionReview(t, a, app, "hostname-conflict", nil)
	other := core.App{ID: "route-conflict-owner", ProjectID: app.ProjectID, ServerID: app.ServerID, Name: "Existing owner", Domain: review.Clone.Domain, ContainerPort: 8080, BuildType: core.BuildTypeDockerfile, CreatedAt: time.Now().UTC()}
	if err = a.store.CreateApp(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.(store.ApplicationRouteStore).ReserveApplicationRoute(ctx, core.ApplicationRoute{AppID: other.ID, ProjectID: other.ProjectID, ServerID: other.ServerID, Hostname: other.Domain, EntryPoint: "web", RequestedDeploymentID: "existing-route-deployment"}); err != nil {
		t.Fatal(err)
	}
	w := temporaryKeyed(a, "/api/v1/temporary-environments", "hostname-conflict-create", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if w.Code != 409 {
		t.Fatal("duplicate hostname accepted", w.Code, w.Body.String())
	}
	if _, err = a.store.GetApp(ctx, review.Clone.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed acceptance left generated app", err)
	}
}

func TestTemporaryEnvironmentReadinessWaitsForManagedDNSTLS(t *testing.T) {
	a, app := temporaryFixture(t)
	ctx := context.Background()
	app.ContainerPort = 8080
	if err := a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	server, err := a.store.GetServer(ctx, app.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	server.Routing = &core.RoutingConfig{BaseDomain: "readiness.example.test", EntryPoint: "websecure", TLSResolver: "letsencrypt", RequireTLS: true}
	if err = a.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	review := temporaryCompositionReview(t, a, app, "route-readiness", nil)
	w := temporaryKeyed(a, "/api/v1/temporary-environments", "create-route-readiness", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	data := a.store.(*store.SQLStore)
	e, err := data.GetTemporaryEnvironment(ctx, review.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := data.GetDeployment(ctx, e.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	d.State = core.DeploymentSucceeded
	d.Health.State = "passed"
	if err = data.UpdateDeploymentHealth(ctx, d.ID, d.Health); err != nil {
		t.Fatal(err)
	}
	if err = data.UpdateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	route, err := data.GetApplicationRoute(ctx, e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	route.State = "published"
	route.DeploymentID = e.DeploymentID
	route.Destination = "http://127.0.0.1:18080"
	if err = data.SaveApplicationRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	a.reconcileTemporaryEnvironments(ctx)
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State == "ready" {
		t.Fatal("unverified public endpoint became ready", e, err)
	}
	route, err = data.GetApplicationRoute(ctx, e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	before := route
	route.State = "active"
	route.DNS = "resolved"
	if err = data.SaveApplicationRouteObservation(ctx, before, route); err != nil {
		t.Fatal(err)
	}
	a.reconcileTemporaryEnvironments(ctx)
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State == "ready" {
		t.Fatal("pending certificate became ready", e, err)
	}
	route, err = data.GetApplicationRoute(ctx, e.AppID)
	if err != nil {
		t.Fatal(err)
	}
	before = route
	route.Certificate.State = "verified"
	if err = data.SaveApplicationRouteObservation(ctx, before, route); err != nil {
		t.Fatal(err)
	}
	a.reconcileTemporaryEnvironments(ctx)
	e, err = data.GetTemporaryEnvironment(ctx, e.ID)
	if err != nil || e.State != "ready" {
		t.Fatal("verified isolated endpoint not ready", e, err)
	}
}

func TestTemporaryEnvironmentRejectsUnversionedExternalServiceCredentials(t *testing.T) {
	a, app := temporaryFixture(t)
	ctx := context.Background()
	service := temporarySharedService(t, a, app.ProjectID)
	now := time.Now().UTC()
	externalStore := core.SecretStore{ID: "environment-external-store", Name: "External", Provider: "vault", Config: map[string]string{}, EncryptedCredentials: "private-fixture-credentials", State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateSecretStore(ctx, externalStore); err != nil {
		t.Fatal(err)
	}
	secret := core.Secret{ID: "environment-service-secret", Name: "Connection", Type: core.SecretTypeText, Source: core.SecretSourceExternal, ExternalStoreID: externalStore.ID, ExternalSecretID: "private-fixture-path", EnvironmentVariable: "ENVIRONMENT_DATABASE_URL", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	service.Fields = map[string]core.ServiceField{"connectionUrl": {SecretRef: secret.ID, Sensitive: true, Configured: true}}
	service.Revision++
	if err := a.store.UpdateService(ctx, service, 1); err != nil {
		t.Fatal(err)
	}
	input := core.TemporaryEnvironmentInput{ProjectID: app.ProjectID, TemplateID: app.ID, ServerID: app.ServerID, Name: "external-secret-review", SourceSHA: strings.Repeat("a", 40), LifetimeSeconds: 3600, ServiceBindings: []core.ServiceBinding{{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}}}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/temporary-environments/review", input, 422)
	if strings.Contains(string(raw), externalStore.EncryptedCredentials) || strings.Contains(string(raw), secret.ExternalSecretID) {
		t.Fatal("credential material exposed in rejection")
	}
	data := a.store.(*store.SQLStore)
	environments, err := data.ListTemporaryEnvironments(ctx, app.ProjectID)
	if err != nil || len(environments) != 0 {
		t.Fatal("rejected credentials created runtime ownership", environments, err)
	}
	// Local references are captured, but switching one to an external source after review must reject acceptance.
	secret.Source = core.SecretSourceLocal
	secret.ExternalStoreID = ""
	secret.ExternalSecretID = ""
	secret.EncryptedValue = "local-fixture-cipher"
	if err = a.store.UpdateSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	review := temporaryCompositionReview(t, a, app, "changed-credential-source", input.ServiceBindings)
	secret.Source = core.SecretSourceExternal
	secret.ExternalStoreID = externalStore.ID
	secret.ExternalSecretID = "private-fixture-path"
	secret.EncryptedValue = ""
	if err = a.store.UpdateSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	w := temporaryKeyed(a, "/api/v1/temporary-environments", "changed-service-secret", temporaryAcceptance{ReviewID: review.ID, Digest: review.Digest, ConfirmName: review.Input.Name})
	if w.Code != 409 {
		t.Fatal("credential source changed after review", w.Code, w.Body.String())
	}
	if _, err = a.store.GetApp(ctx, review.Clone.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("credential rejection left generated app", err)
	}
}

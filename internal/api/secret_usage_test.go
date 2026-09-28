package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
)

func TestSecretUsageIncludesWorkflowsTemplatesAndDeploymentConsumers(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	servers, _ := a.store.ListServers(ctx)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	secret := core.Secret{ID: "bundle", Name: "App bundle", Type: core.SecretTypeJSON, EnvironmentVariable: "APP_BUNDLE", EncryptedValue: "opaque-cipher-must-not-be-read", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateSecret(ctx, secret))
	source := core.ConfigSource{ID: "config", ProjectID: projects[0].ID, CredentialSecretID: secret.ID, Name: "Deployment configuration", Repository: "team/deployments", Branch: "main", Path: "deployment", State: "ready", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateConfigSource(ctx, source))
	document := `apiVersion: dispatch/v1alpha1
kind: Application
metadata: {name: checkout}
spec:
  jobs:
    build:
      secrets:
        TOKEN: {secretRef: app BUNDLE, key: auth.token}
        URL: {secretRef: bundle, key: connection.URL}
  finally:
    cleanup:
      secrets:
        TOKEN: {secretRef: bundle, key: auth.token}
  stages:
    - name: development
      checks:
        smoke: {pipelineRef: smoke}
`
	resource := core.WorkflowResource{ID: "checkout", ConfigSourceID: source.ID, Kind: "Application", Name: "Checkout", Document: document, Path: "deployment/app.yaml", State: "ready", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateWorkflowResource(ctx, resource))
	pipeline := core.WorkflowResource{ID: "smoke", ConfigSourceID: source.ID, Kind: "Pipeline", Name: "smoke", Document: "spec: {jobs: {check: {secrets: {TOKEN: {secretRef: bundle}}}}}", Path: "deployment/smoke.yaml", State: "ready", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateWorkflowResource(ctx, pipeline))
	archived := resource
	archived.ID = "old-checkout"
	archived.Name = "Old checkout"
	archived.Path = "deployment/old.yaml"
	archived.State = "removed"
	must(a.store.CreateWorkflowResource(ctx, archived))
	github := core.GitHubAppConnection{ID: "github", Name: "GitHub", State: "ready", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateGitHubApp(ctx, github))
	must(a.store.CreateWorkflowPreviewTemplate(ctx, core.WorkflowPreviewTemplate{ID: "previews", ConfigSourceID: source.ID, GitHubAppID: github.ID, Name: "PR previews", Document: document, CreatedAt: now, UpdatedAt: now}))
	must(a.store.CreateSavedServiceTemplate(ctx, core.SavedServiceTemplate{ID: "database-template", ProjectID: projects[0].ID, Name: "Database template", Revision: 1, Document: "spec: {provision: {secrets: {TOKEN: {secretRef: bundle}}}}", CreatedAt: now, UpdatedAt: now}))
	app := core.App{ID: "consumer", ProjectID: projects[0].ID, ServerID: servers[0].ID, Name: "Checkout development", Generated: true, BuildType: core.BuildTypeDockerfile, State: "idle", HelmProvenance: core.HelmProvenance{WorkflowResourceID: resource.ID}, SourceAuthType: "ssh_key", SourceCredentialID: secret.ID, HookEnvironment: map[string]string{core.SecretEnvironmentKey(secret.ID, "TOKEN"): "opaque-hook-cipher"}, CreatedAt: now}
	must(a.store.CreateApp(ctx, app))
	must(a.store.CreateServer(ctx, core.Server{ID: "builder", Name: "Build host", Runtime: core.ServerRuntimeBuilder, Builder: &core.BuilderServerConfig{SSHSecretID: secret.ID}, CreatedAt: now}))
	must(a.store.CreateService(ctx, core.Service{ID: "database", Name: "Database", ProjectID: projects[0].ID, Type: "generic", Fields: map[string]core.ServiceField{"token": {SecretRef: secret.ID, Sensitive: true, Configured: true}}, Revision: 1, CreatedAt: now, UpdatedAt: now}))
	must(a.store.ReplaceAppServiceBindings(ctx, app.ID, []core.ServiceBinding{{Alias: "database", ServiceRef: "database", Environment: map[string]string{"TOKEN": "token"}}}))
	raw := serviceRequestTest(t, a, "GET", "/api/v1/secrets/usage", nil, 200)
	if strings.Contains(string(raw), "opaque-") || strings.Contains(string(raw), "secretRef") {
		t.Fatalf("usage contains credential data or raw configuration: %s", raw)
	}
	var usages []secretUsage
	must(json.Unmarshal(raw, &usages))
	var usage secretUsage
	for _, item := range usages {
		if item.SecretID == secret.ID {
			usage = item
		}
	}
	if len(usage.Consumers) != 8 || len(usage.Archived) != 1 || len(usage.Warnings) != 0 {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	for _, consumer := range usage.Consumers {
		if consumer.ID == resource.ID && (len(consumer.References) != 3 || len(consumer.Applications) != 1) {
			t.Fatalf("workflow references or deployment mapping missing: %#v", consumer)
		}
		if consumer.ID == pipeline.ID && len(consumer.Applications) != 1 {
			t.Fatalf("check pipeline deployment mapping missing: %#v", consumer)
		}
		if consumer.ID == app.ID && len(consumer.References) != 2 {
			t.Fatalf("application counted more than once or hooks missing: %#v", consumer)
		}
	}
	// A paginated history query must not rely on the overview's recent 100 runs,
	// and must not return snapshots, credentials, outputs, or unrelated apps.
	for n := 0; n < 55; n++ {
		must(a.store.CreateDeployment(ctx, core.Deployment{ID: fmt.Sprintf("related-%03d", n), AppID: app.ID, State: core.DeploymentSucceeded, CommitSHA: fmt.Sprintf("revision-%03d", n), CreatedAt: now.Add(time.Duration(n) * time.Second), Outputs: map[string]string{"private": "do-not-return"}}))
	}
	pageRaw := serviceRequestTest(t, a, "GET", "/api/v1/secrets/bundle/usage/deployments", nil, 200)
	var page struct {
		Items []core.Deployment `json:"items"`
		Next  string            `json:"next"`
	}
	must(json.Unmarshal(pageRaw, &page))
	if len(page.Items) != 50 || page.Next == "" || strings.Contains(string(pageRaw), "do-not-return") {
		t.Fatalf("invalid history page: %s", pageRaw)
	}
	second := serviceRequestTest(t, a, "GET", "/api/v1/secrets/bundle/usage/deployments?before="+page.Next, nil, 200)
	must(json.Unmarshal(second, &page))
	if len(page.Items) != 5 {
		t.Fatalf("expected remaining 5 deployments: %s", second)
	}
}

func TestSecretUsageDoesNotPresentUnreadableConfigurationAsZero(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	projects, _ := a.store.ListProjects(ctx)
	for _, err := range []error{
		a.store.CreateSecret(ctx, core.Secret{ID: "unused", Name: "Unused", EnvironmentVariable: "UNUSED", Type: core.SecretTypeText, CreatedAt: now, UpdatedAt: now}),
		a.store.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: projects[0].ID, Name: "Configuration", CredentialSecretID: "unused", CreatedAt: now, UpdatedAt: now}),
		a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "broken", ConfigSourceID: "config", Name: "Broken workflow", Document: "spec: [", Path: "broken.yaml", CreatedAt: now, UpdatedAt: now}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/secrets/usage", nil, 200)
	var items []secretUsage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Warnings) != 1 || !strings.Contains(items[0].Warnings[0], "Broken workflow") {
		t.Fatalf("missing warning: %s", raw)
	}
	serviceRequestTest(t, a, "GET", "/api/v1/secrets/missing/usage/deployments", nil, 404)
	user := core.User{ID: "member", Username: "member", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	token, err := a.createSession(ctx, user.ID, identityForUser(user))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/secrets/usage", "/api/v1/secrets/unused/usage/deployments"} {
		for _, auth := range []string{"", "Bearer " + token} {
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", auth)
			rr := httptest.NewRecorder()
			a.ServeHTTP(rr, r)
			expected := 401
			if auth != "" {
				expected = 403
			}
			if rr.Code != expected {
				t.Fatalf("%s: expected %d, got %d", path, expected, rr.Code)
			}
		}
	}
}

func TestUsageDocumentFindsNestedTemplatesAndIgnoresScriptText(t *testing.T) {
	document := usageDocument{}
	// Template references must be parsed as YAML fields, not found by text search.
	raw := `spec:
  template:
    spec:
      jobs:
        build:
          run: 'echo "secretRef: fake"'
          secrets:
            CONFIG: {secretRef: bundle, key: nested.URL}
`
	if err := yaml.Unmarshal([]byte(raw), &document); err != nil {
		t.Fatal(err)
	}
	refs := document.references()
	if len(refs) != 1 || len(refs["bundle"]) != 1 || !strings.Contains(refs["bundle"][0], "nested.URL") {
		t.Fatalf("unexpected refs: %#v", refs)
	}
}

func TestSecretUsageIncludesClosedGeneratedApplications(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	projects, _ := a.store.ListProjects(ctx)
	servers, _ := a.store.ListServers(ctx)
	if err := a.store.CreateSecret(ctx, core.Secret{ID: "checkout-key", Name: "Checkout key", EnvironmentVariable: "CHECKOUT_KEY", Type: core.SecretTypeSSHPrivateKey, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateApp(ctx, core.App{ID: "closed-preview", ProjectID: projects[0].ID, ServerID: servers[0].ID, Name: "Closed preview", Generated: true, State: "closed", SourceAuthType: "ssh_key", SourceCredentialID: "checkout-key", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	raw := serviceRequestTest(t, a, "GET", "/api/v1/secrets/usage", nil, 200)
	var items []secretUsage
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Consumers) != 0 || len(items[0].Archived) != 1 || !items[0].Archived[0].Applications[0].Archived {
		t.Fatalf("closed generated app missing: %s", raw)
	}
}

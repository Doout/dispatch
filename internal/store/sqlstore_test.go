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

func TestPostgresMigrationsWhenConfigured(t *testing.T) {
	databaseURL := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if databaseURL == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL to exercise PostgreSQL migrations")
	}
	ctx := context.Background()
	data, err := Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		t.Fatalf("migrations must be idempotent: %v", err)
	}
	var count int
	if err := data.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='008_preview_groups'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected preview group migration once, got %d", count)
	}
	if err := data.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='011_application_templates'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected application template migration once, got %d", count)
	}
	if err := data.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version='012_openshift_servers'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected OpenShift server migration once, got %d", count)
	}
}

func TestSQLiteAdminCredentialIsSingleUse(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	credential := AdminCredential{Username: "operator", PasswordHash: "bcrypt-hash", CreatedAt: time.Now().UTC()}
	if err := data.CreateAdminCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetAdminCredential(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Username != credential.Username || stored.PasswordHash != credential.PasswordHash {
		t.Fatalf("unexpected stored credential: %#v", stored)
	}
	if err := data.CreateAdminCredential(ctx, credential); err != ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestSQLiteGitHubAppConnectionRoundTrip(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "github-apps.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	item := core.GitHubAppConnection{
		ID: "github-app-1", Name: "Engineering GitHub", WebURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3",
		AppID: 42, ClientID: "Iv1.test", Slug: "dispatch-platform", RegistrationOwner: "platform", RegistrationOwnerType: "Organization",
		InstallationID: 73, InstallationAccount: "platform",
		WebhookURL: "https://dispatch.example/api/v1/events/github/apps/github-app-1", EncryptedPrivateKey: "private-ciphertext",
		EncryptedWebhookSecret: "webhook-ciphertext", State: "ready", LastVerifiedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := data.CreateGitHubApp(ctx, item); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetGitHubApp(ctx, item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != item.Name || stored.RegistrationOwner != "platform" || stored.RegistrationOwnerType != "Organization" || stored.InstallationID != item.InstallationID || !stored.PrivateKeyConfigured || !stored.WebhookSecretConfigured {
		t.Fatalf("unexpected GitHub App connection: %#v", stored)
	}
	stored.InstallationAccount = "platform-tools"
	stored.UpdatedAt = now.Add(time.Minute)
	if err := data.UpdateGitHubApp(ctx, stored); err != nil {
		t.Fatal(err)
	}
	items, err := data.ListGitHubApps(ctx)
	if err != nil || len(items) != 1 || items[0].InstallationAccount != "platform-tools" {
		t.Fatalf("unexpected GitHub App list: %#v, %v", items, err)
	}
	if err := data.DeleteGitHubApp(ctx, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetGitHubApp(ctx, item.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected deleted GitHub App to be missing, got %v", err)
	}
}

func TestSQLiteAdminSessionsSurviveStoreReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	data, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := data.CreateAdminSession(ctx, "hashed-token", now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	if err := data.Close(); err != nil {
		t.Fatal(err)
	}

	data, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	valid, err := data.AdminSessionValid(ctx, "hashed-token", now.Add(time.Minute))
	if err != nil || !valid {
		t.Fatalf("persisted session was not valid: valid=%v err=%v", valid, err)
	}
	valid, err = data.AdminSessionValid(ctx, "hashed-token", now.Add(2*time.Hour))
	if err != nil || valid {
		t.Fatalf("expired session remained valid: valid=%v err=%v", valid, err)
	}
}

func TestSQLiteMigrationAndDemoSeed(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := data.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	if err := data.SeedDemo(ctx); err != nil {
		t.Fatalf("seed must be idempotent: %v", err)
	}

	projects, err := data.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	servers, err := data.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	apps, err := data.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	deployments, err := data.ListDeployments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || len(servers) != 1 || len(apps) != 3 || len(deployments) != 3 {
		t.Fatalf("unexpected inventory sizes: projects=%d servers=%d apps=%d deployments=%d", len(projects), len(servers), len(apps), len(deployments))
	}
	if deployments[0].App == nil || deployments[0].Server == nil {
		t.Fatal("deployment evidence was not hydrated")
	}
	logs, err := data.ListDeploymentLogs(ctx, deployments[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 {
		t.Fatalf("expected 3 evidence log entries, got %d", len(logs))
	}
}

func TestSQLiteNotFound(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetApp(ctx, "missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteApplicationTemplateRoundTrip(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "template-project", Name: "Templates", CreatedAt: now}
	server := core.Server{ID: "template-server", Name: "Docker", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "template-app", ProjectID: project.ID, ServerID: server.ID, Name: "Preview service", SourceRepo: "git@github.com:example/app.git", Branch: "main", SourceAuthType: "ssh_key", SourceCredentialID: "deploy-key", BuildType: core.BuildTypeCompose, ComposeContent: "services:\n  app:\n    image: example/app", Template: true, State: "template", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetApp(ctx, app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Template || stored.State != "template" || stored.ComposeContent != app.ComposeContent || stored.SourceAuthType != "ssh_key" || stored.SourceCredentialID != "deploy-key" {
		t.Fatalf("unexpected stored template: %#v", stored)
	}
	stored.Name = "Updated template"
	if err := data.UpdateApp(ctx, stored); err != nil {
		t.Fatal(err)
	}
	apps, err := data.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || !apps[0].Template || apps[0].Name != "Updated template" {
		t.Fatalf("unexpected template inventory: %#v", apps)
	}
}

func TestSQLiteEnsuresOneManagedLocalDockerServer(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	first, err := data.EnsureLocalDockerServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first.Name = "changed"
	first.Address = "LOCAL"
	first.Runtime = "other"
	first.State = "pending"
	first.AgentMode = "ssh-bootstrap"
	if err := data.UpdateServer(ctx, first); err != nil {
		t.Fatal(err)
	}

	second, err := data.EnsureLocalDockerServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Name != "local-docker" || second.Address != "local" || second.Runtime != "docker" || second.State != "ready" || second.AgentMode != "local" {
		t.Fatalf("unexpected reconciled local server: %#v", second)
	}
	servers, err := data.ListServers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected one local server after repeated discovery, got %d", len(servers))
	}
}

func TestSQLiteUsesAvailableNameForManagedLocalDockerServer(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index, name := range []string{"local-docker", "controller-docker"} {
		server := core.Server{ID: "remote-" + string(rune('a'+index)), Name: name, Address: "10.0.0.8", Runtime: "docker", State: "pending", AgentMode: "ssh-bootstrap", CreatedAt: now}
		if err := data.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}

	local, err := data.EnsureLocalDockerServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if local.Name != "controller-docker-2" {
		t.Fatalf("expected conflict-free managed name, got %q", local.Name)
	}
}

func TestSQLiteMarksManagedLocalDockerUnavailableWithoutSocket(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ready, err := data.ReconcileLocalDockerServer(ctx, true)
	if err != nil || ready == nil || ready.State != "ready" {
		t.Fatalf("expected ready managed server, got %#v err=%v", ready, err)
	}
	unavailable, err := data.ReconcileLocalDockerServer(ctx, false)
	if err != nil || unavailable == nil || unavailable.ID != ready.ID || unavailable.State != "unavailable" {
		t.Fatalf("expected unavailable managed server, got %#v err=%v", unavailable, err)
	}
	restored, err := data.ReconcileLocalDockerServer(ctx, true)
	if err != nil || restored == nil || restored.ID != ready.ID || restored.State != "ready" {
		t.Fatalf("expected restored managed server, got %#v err=%v", restored, err)
	}
}

func TestSQLiteProjectAndServerLifecycle(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "project-1", Name: "Platform", Description: "Initial", CreatedAt: now}
	server := core.Server{ID: "server-1", Name: "Build", Address: "10.0.0.8", Runtime: "docker", State: "pending", AgentMode: "ssh-bootstrap", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	project.Name, project.Description = "Services", "Updated"
	server.Name, server.Address = "Build east", "10.0.0.9"
	if err := data.UpdateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	storedProject, err := data.GetProject(ctx, project.ID)
	if err != nil || storedProject.Name != "Services" || storedProject.Description != "Updated" {
		t.Fatalf("unexpected project after update: %#v, err=%v", storedProject, err)
	}
	storedServer, err := data.GetServer(ctx, server.ID)
	if err != nil || storedServer.Name != "Build east" || storedServer.Address != "10.0.0.9" {
		t.Fatalf("unexpected server after update: %#v, err=%v", storedServer, err)
	}
	if err := data.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteProject(ctx, project.ID); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteServer(ctx, server.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting missing server, got %v", err)
	}
	if err := data.DeleteProject(ctx, project.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting missing project, got %v", err)
	}
}

func TestSQLitePreviewEventLifecycleIsIdempotent(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "project-preview", Name: "Preview", CreatedAt: now}
	server := core.Server{ID: "server-preview", Name: "Preview", Address: "local", Runtime: "docker", State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "app-preview", ProjectID: project.ID, ServerID: server.ID, Name: "Preview template", SourceRepo: "acme/checkout", Branch: "main", BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile", State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	secret := core.Secret{ID: "secret-registry", Name: "Registry token", Type: core.SecretTypeRegistryPassword, EnvironmentVariable: "REGISTRY_TOKEN", PublicValue: "public-metadata", EncryptedValue: "encrypted-token", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateSecret(ctx, secret); err != nil {
		t.Fatal(err)
	}
	storedSecret, err := data.GetSecret(ctx, secret.ID)
	if err != nil || storedSecret.Type != secret.Type || storedSecret.PublicValue != secret.PublicValue {
		t.Fatalf("typed secret metadata was not persisted: %#v err=%v", storedSecret, err)
	}
	trigger := core.EventTrigger{ID: "trigger-preview", AppID: app.ID, GitHubAppID: "connector-a", Provider: core.EventProviderGitHub, Repository: "acme/checkout", Command: "/preview", Enabled: true,
		PreDeployHook: "echo pre", PostDeployHook: "echo post", SecretIDs: []string{secret.ID}, CreatedAt: now, UpdatedAt: now}
	storedTrigger, created, err := data.CreateEventTrigger(ctx, trigger)
	if err != nil || !created || storedTrigger.ID != trigger.ID {
		t.Fatalf("unexpected trigger creation: trigger=%#v created=%v err=%v", storedTrigger, created, err)
	}
	storedTrigger, created, err = data.CreateEventTrigger(ctx, trigger)
	if err != nil || created || storedTrigger.ID != trigger.ID {
		t.Fatalf("repeated trigger creation must be idempotent: trigger=%#v created=%v err=%v", storedTrigger, created, err)
	}
	if matches, err := data.HasEventTrigger(ctx, core.EventProviderGitHub, "acme/checkout", "/preview", "connector-a"); err != nil || !matches {
		t.Fatalf("expected trigger to match its connector: matches=%v err=%v", matches, err)
	}
	if matches, err := data.HasEventTrigger(ctx, core.EventProviderGitHub, "acme/checkout", "/preview", "connector-b"); err != nil || matches {
		t.Fatalf("trigger must not match another connector: matches=%v err=%v", matches, err)
	}

	comment := core.IncomingEvent{ID: "event-comment", Provider: core.EventProviderGitHub, ProviderConnectionID: "connector-a", DeliveryID: "delivery-comment", Kind: core.EventKindPullRequestComment, Action: "created", Repository: "acme/checkout", PullRequestNumber: 17, HeadRef: "feature/cart", HeadSHA: "abc123", BaseRef: "main", Actor: "octo", ActorAssociation: "MEMBER", TrustedActor: true, Command: "/preview", SourceCommentID: "501", ReceivedAt: now}
	result, err := data.ProcessIncomingEvent(ctx, comment)
	if err != nil || result.Duplicate || len(result.Previews) != 1 {
		t.Fatalf("unexpected preview event result: %#v err=%v", result, err)
	}
	preview := result.Previews[0]
	if preview.TemplateAppID != app.ID || preview.AppID != "" || preview.HeadRef != "feature/cart" || preview.HeadSHA != "abc123" || preview.State != core.PreviewRequested {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if preview.PreDeployHook != "echo pre" || preview.PostDeployHook != "echo post" || preview.HookEnvironment["DISPATCH_EVENT_ACTOR"] != "octo" {
		t.Fatalf("event hook snapshot was not persisted: %#v", preview)
	}
	if preview.HookEnvironment[core.SecretEnvironmentKey(secret.ID, secret.EnvironmentVariable)] != secret.EncryptedValue {
		t.Fatalf("encrypted event secret was not snapshotted: %#v", preview.HookEnvironment)
	}
	result, err = data.ProcessIncomingEvent(ctx, comment)
	if err != nil || !result.Duplicate || len(result.Previews) != 1 || result.Previews[0].ID != preview.ID {
		t.Fatalf("delivery retry must return the existing preview: %#v err=%v", result, err)
	}

	closedAt := now.Add(time.Minute)
	closed := core.IncomingEvent{ID: "event-close", Provider: core.EventProviderGitHub, ProviderConnectionID: "connector-a", DeliveryID: "delivery-close", Kind: core.EventKindPullRequest, Action: "closed", Repository: "acme/checkout", PullRequestNumber: 17, HeadRef: "feature/cart", HeadSHA: "def456", BaseRef: "main", Actor: "octo", ReceivedAt: closedAt}
	result, err = data.ProcessIncomingEvent(ctx, closed)
	if err != nil || len(result.Previews) != 1 {
		t.Fatalf("unexpected close result: %#v err=%v", result, err)
	}
	if result.Previews[0].State != core.PreviewCleanupRequested || result.Previews[0].ClosedAt == nil || result.Previews[0].HeadSHA != "def456" {
		t.Fatalf("close must request cleanup: %#v", result.Previews[0])
	}
}

func TestSQLiteEventTriggerCannotDeleteActivePreview(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "project-trigger", Name: "Trigger", CreatedAt: now}
	server := core.Server{ID: "server-trigger", Name: "Trigger", Address: "local", Runtime: "docker", State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "app-trigger", ProjectID: project.ID, ServerID: server.ID, Name: "Trigger", SourceRepo: "https://example.test/repo.git", Branch: "main", BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile", State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	trigger := core.EventTrigger{ID: "active-trigger", AppID: app.ID, Provider: core.EventProviderGitHub, Repository: "acme/app", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if _, _, err := data.CreateEventTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	event := core.IncomingEvent{ID: "active-event", Provider: core.EventProviderGitHub, DeliveryID: "active-delivery", Kind: core.EventKindPullRequestComment, Action: "created", Repository: "acme/app", PullRequestNumber: 8, Actor: "member", ActorAssociation: "MEMBER", TrustedActor: true, Command: "/preview", ReceivedAt: now}
	result, err := data.ProcessIncomingEvent(ctx, event)
	if err != nil || len(result.Previews) != 1 {
		t.Fatalf("unexpected preview creation: %#v err=%v", result, err)
	}
	if err := data.DeleteEventTrigger(ctx, trigger.ID); !errors.Is(err, ErrEventTriggerActive) {
		t.Fatalf("expected active trigger conflict, got %v", err)
	}
	preview := result.Previews[0]
	preview.State, preview.UpdatedAt = core.PreviewClosed, time.Now().UTC()
	if changed, err := data.TransitionPreviewEnvironment(ctx, preview, core.PreviewRequested); err != nil || !changed {
		t.Fatalf("close preview: changed=%v err=%v", changed, err)
	}
	if err := data.DeleteEventTrigger(ctx, trigger.ID); err != nil {
		t.Fatalf("closed trigger should be deletable: %v", err)
	}
}

func TestSQLiteKubernetesServerLifecycle(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	server := core.Server{
		ID: "server-kubernetes", Name: "Production cluster", Address: "/etc/dispatch/kubeconfig",
		Runtime: core.ServerRuntimeKubernetes, State: "ready", AgentMode: "direct", CreatedAt: now,
		Kubernetes: &core.KubernetesServerConfig{
			KubeconfigPath: "/etc/dispatch/kubeconfig", Context: "production", Namespace: "previews",
		},
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	stored, err := data.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Kubernetes == nil || *stored.Kubernetes != *server.Kubernetes {
		t.Fatalf("unexpected Kubernetes configuration: %#v", stored.Kubernetes)
	}

	stored.Kubernetes.Context = "preview"
	stored.Kubernetes.Namespace = "pull-requests"
	stored.Kubernetes.KubeconfigPath = ""
	stored.Kubernetes.KubeconfigData = "apiVersion: v1\nkind: Config\n"
	stored.Kubernetes.CertificateAuthorityData = "test-ca"
	if err := data.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err := data.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kubernetes == nil || updated.Kubernetes.Context != "preview" || updated.Kubernetes.Namespace != "pull-requests" ||
		updated.Kubernetes.KubeconfigData != stored.Kubernetes.KubeconfigData || updated.Kubernetes.CertificateAuthorityData != "test-ca" ||
		!updated.Kubernetes.KubeconfigStored || !updated.Kubernetes.CertificateAuthorityStored {
		t.Fatalf("unexpected updated Kubernetes configuration: %#v", updated.Kubernetes)
	}
}

func TestSQLiteOpenShiftServerLifecycle(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	server := core.Server{ID: "openshift", Name: "OpenShift", Address: "https://api.example.test:6443",
		Runtime: core.ServerRuntimeOpenShift, State: "ready", AgentMode: "direct", CreatedAt: now,
		Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "apiVersion: v1", Context: "dispatch-openshift", Namespace: "previews",
			OpenShift: &core.OpenShiftServerConfig{Managed: true, ServiceAccount: "dispatch-controller",
				ServiceAccountNamespace: "dispatch-system", TokenSecret: "dispatch-controller-token", ConnectedAt: &now}}}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Runtime != core.ServerRuntimeOpenShift || stored.Kubernetes == nil || stored.Kubernetes.OpenShift == nil ||
		!stored.Kubernetes.OpenShift.Managed || stored.Kubernetes.OpenShift.ServiceAccount != "dispatch-controller" ||
		stored.Kubernetes.OpenShift.ConnectedAt == nil || !stored.Kubernetes.OpenShift.ConnectedAt.Equal(now) {
		t.Fatalf("unexpected stored OpenShift server: %#v", stored)
	}
	stored.Kubernetes.Namespace = "updated"
	if err := data.UpdateServer(ctx, stored); err != nil {
		t.Fatal(err)
	}
	updated, err := data.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Kubernetes.Namespace != "updated" || updated.Kubernetes.OpenShift.TokenSecret != "dispatch-controller-token" {
		t.Fatalf("unexpected updated OpenShift server: %#v", updated)
	}
}

func TestSQLiteDeleteAppRemovesDeploymentHistoryTransactionally(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "delete-project", Name: "Delete", CreatedAt: now}
	server := core.Server{ID: "delete-server", Name: "Delete", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "delete-app", ProjectID: project.ID, ServerID: server.ID, Name: "Delete", BuildType: core.BuildTypeCompose, State: "ready", CreatedAt: now}
	deployment := core.Deployment{ID: "delete-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if err := data.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: deployment.ID, Level: "info", Message: "done", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteApp(ctx, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetApp(ctx, app.ID); err != ErrNotFound {
		t.Fatalf("expected deleted application, got %v", err)
	}
	deployments, err := data.ListDeployments(ctx, 100)
	if err != nil || len(deployments) != 0 {
		t.Fatalf("expected deployment history removed, got %d err=%v", len(deployments), err)
	}
	logs, err := data.ListDeploymentLogs(ctx, deployment.ID, 0)
	if err != nil || len(logs) != 0 {
		t.Fatalf("expected deployment logs removed, got %d err=%v", len(logs), err)
	}
	if err := data.DeleteServer(ctx, server.ID); err != nil {
		t.Fatalf("server should be deletable after app removal: %v", err)
	}
	if err := data.DeleteProject(ctx, project.ID); err != nil {
		t.Fatalf("project should be deletable after app removal: %v", err)
	}
}

func TestSQLiteDeleteAppRejectsActiveDeployment(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "active-project", Name: "Active", CreatedAt: now}
	server := core.Server{ID: "active-server", Name: "Active", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "active-app", ProjectID: project.ID, ServerID: server.ID, Name: "Active", BuildType: core.BuildTypeCompose, State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateDeployment(ctx, core.Deployment{ID: "active-deployment", AppID: app.ID, State: core.DeploymentStarting, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteApp(ctx, app.ID); err != ErrAppActive {
		t.Fatalf("expected ErrAppActive, got %v", err)
	}
	if _, err := data.GetApp(ctx, app.ID); err != nil {
		t.Fatalf("active application must be preserved: %v", err)
	}
}

func TestSQLiteDeleteAppRejectsActiveGeneratedPreview(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "preview-delete-project", Name: "Preview delete", CreatedAt: now}
	server := core.Server{ID: "preview-delete-server", Name: "Preview delete", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "preview-delete-app", ProjectID: project.ID, ServerID: server.ID, Name: "Preview delete", BuildType: core.BuildTypeDockerfile, State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	trigger := core.EventTrigger{ID: "preview-delete-trigger", AppID: app.ID, Provider: core.EventProviderGitHub, Repository: "acme/delete", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if _, _, err := data.CreateEventTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	event := core.IncomingEvent{ID: "preview-delete-event", Provider: core.EventProviderGitHub, DeliveryID: "preview-delete-delivery", Kind: core.EventKindPullRequestComment, Action: "created", Repository: "acme/delete", PullRequestNumber: 7, Actor: "operator", ActorAssociation: "MEMBER", TrustedActor: true, Command: "/preview", ReceivedAt: now}
	result, err := data.ProcessIncomingEvent(ctx, event)
	if err != nil || len(result.Previews) != 1 {
		t.Fatalf("expected active preview, got %#v err=%v", result, err)
	}
	if err := data.DeleteApp(ctx, app.ID); err != ErrAppActive {
		t.Fatalf("expected ErrAppActive for template with live preview, got %v", err)
	}
	if _, err := data.GetApp(ctx, app.ID); err != nil {
		t.Fatalf("preview template must be preserved: %v", err)
	}
}

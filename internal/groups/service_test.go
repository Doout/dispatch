package groups

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/store"
)

type fakeResolver struct{}

func (fakeResolver) ResolvePullRequest(_ context.Context, repository string, number int) (events.SourceRevision, error) {
	short := strings.TrimPrefix(repository[strings.LastIndex(repository, "/"):], "/")
	return events.SourceRevision{HeadRef: "pr-" + short, HeadSHA: short + "-pr-sha-" + string(rune('a'+number)), BaseRef: "main", Open: true}, nil
}
func (fakeResolver) ResolveBranch(_ context.Context, repository, branch string) (events.SourceRevision, error) {
	short := strings.TrimPrefix(repository[strings.LastIndex(repository, "/"):], "/")
	return events.SourceRevision{HeadRef: branch, HeadSHA: short + "-default-sha", Open: true}, nil
}

type recordingExecutor struct {
	mu      sync.Mutex
	apps    []core.App
	failSHA string
	data    store.Store
}

func (e *recordingExecutor) Deploy(_ context.Context, deployment core.Deployment, app core.App, _ core.Server, progress deploy.Progress) error {
	e.mu.Lock()
	e.apps = append(e.apps, app)
	fail := deployment.CommitSHA == e.failSHA
	e.mu.Unlock()
	if fail {
		return errors.New("injected deployment failure")
	}
	if e.data != nil {
		switch {
		case strings.HasSuffix(app.HelmRelease, "-service"):
			_ = e.data.UpdateDeploymentOutputs(context.Background(), deployment.ID, map[string]string{"url": "https://published-service.example.test", "imageDigest": "sha256:service"})
		case strings.HasSuffix(app.HelmRelease, "-ui"):
			_ = e.data.UpdateDeploymentOutputs(context.Background(), deployment.ID, map[string]string{"url": "https://published-ui.example.test"})
		}
	}
	for _, state := range []core.DeploymentState{core.DeploymentFetching, core.DeploymentBuilding, core.DeploymentStarting, core.DeploymentChecking, core.DeploymentRouting} {
		if err := progress(state, "ok"); err != nil {
			return err
		}
	}
	return nil
}
func (*recordingExecutor) Cleanup(_ context.Context, _ core.App, _ core.Server, progress deploy.Progress) error {
	return progress(core.DeploymentSucceeded, "removed")
}

type noNamespaceCleanup struct{}

func (noNamespaceCleanup) CleanupNamespace(context.Context, core.Server, string) error { return nil }

type recordingNotifier struct {
	mu     sync.Mutex
	bodies map[string]string
}

func (n *recordingNotifier) UpdateComment(_ context.Context, repository string, number int, commentID, body string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.bodies == nil {
		n.bodies = map[string]string{}
	}
	key := repository + "#" + strconv.Itoa(number)
	n.bodies[key] = body
	if commentID != "" {
		return commentID, nil
	}
	return "comment-" + key, nil
}

func TestLinkedGroupDeploysDependenciesAndKeepsStableURLWhenPRIsAttached(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "project", Name: "Preview", CreatedAt: now}
	server := core.Server{ID: "cluster", Name: "Cluster", Runtime: core.ServerRuntimeKubernetes, State: "ready", AgentMode: "direct",
		Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/tmp/kubeconfig"}, CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	for _, app := range []core.App{
		{ID: "service-app", ProjectID: project.ID, ServerID: server.ID, Name: "Service", SourceRepo: "https://example.test/org/service", BuildType: core.BuildTypeHelm, HelmChart: "oci://charts/service", Domain: "service-{preview}.example.test", State: "ready", CreatedAt: now},
		{ID: "ui-app", ProjectID: project.ID, ServerID: server.ID, Name: "UI", SourceRepo: "https://example.test/org/ui", BuildType: core.BuildTypeHelm, HelmChart: "oci://charts/ui", Domain: "preview-{pr}.example.test", State: "ready", CreatedAt: now},
	} {
		if err := data.CreateApp(ctx, app); err != nil {
			t.Fatal(err)
		}
	}
	registrySecret := core.Secret{ID: "registry-secret", Name: "Registry key", Type: core.SecretTypeRegistryPassword, EnvironmentVariable: "IBMCLOUD_API_KEY", EncryptedValue: "encrypted-registry-key", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateSecret(ctx, registrySecret); err != nil {
		t.Fatal(err)
	}
	group := core.PreviewGroup{ID: "group", Name: "Full stack", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now,
		Components: []core.PreviewGroupComponent{
			{ID: "service", GroupID: "group", AppID: "service-app", Alias: "service", Repository: "org/service", DefaultBranch: "main", PreDeployHook: "echo build service", SecretIDs: []string{registrySecret.ID}},
			{ID: "ui", GroupID: "group", AppID: "ui-app", Alias: "ui", Repository: "org/ui", DefaultBranch: "main", Entrypoint: true, DependsOn: []string{"service"}, PostDeployHook: "echo publish ui", Bindings: []core.PreviewGroupBinding{{Source: "service.url", HelmValuePath: "config.backendUrl"}}},
		}}
	if err := Validate(ctx, data, &group, "/preview"); err != nil {
		t.Fatal(err)
	}
	if err := data.CreatePreviewGroup(ctx, group); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{data: data}
	notifier := &recordingNotifier{}
	service := New(data, deploy.NewService(data, executor), fakeResolver{}, notifier, noNamespaceCleanup{})
	service.pollEvery = time.Millisecond
	event := core.IncomingEvent{Provider: core.EventProviderGitHub, DeliveryID: "delivery-1", Kind: core.EventKindPullRequestComment,
		Action: "created", Repository: "org/service", PullRequestNumber: 11, TrustedActor: true, Command: "/preview", ReceivedAt: now}
	runs, err := service.Process(ctx, event)
	if err != nil || len(runs) != 1 {
		t.Fatalf("start group: runs=%#v err=%v", runs, err)
	}
	first := waitForGroupState(t, data, runs[0].ID, core.PreviewGroupReady)
	if duplicate, err := service.Process(ctx, event); err != nil || len(duplicate) != 0 {
		t.Fatalf("duplicate delivery was not ignored: %#v err=%v", duplicate, err)
	}
	if err := data.DeletePreviewGroup(ctx, group.ID); err != store.ErrPreviewGroupActive {
		t.Fatalf("expected active group deletion conflict, got %v", err)
	}
	if len(first.Sources) != 2 || sourceForAlias(first.Sources, "service").SHA != "service-pr-sha-l" || sourceForAlias(first.Sources, "ui").SHA != "ui-default-sha" {
		t.Fatalf("exact source set was not persisted: %#v", first.Sources)
	}
	stableURL := first.EntrypointURL

	executor.mu.Lock()
	if len(executor.apps) != 2 {
		t.Fatalf("expected two component deployments, got %d", len(executor.apps))
	}
	serviceApp, uiApp := executor.apps[0], executor.apps[1]
	executor.mu.Unlock()
	if !strings.Contains(uiApp.HelmGroupValues, "backendUrl") || !strings.Contains(uiApp.HelmGroupValues, "published-service") {
		t.Fatalf("service output was not bound into UI values: %q", uiApp.HelmGroupValues)
	}
	if uiApp.HookEnvironment["DISPATCH_COMPONENT_SERVICE_URL"] != "https://published-service.example.test" || uiApp.HookEnvironment["DISPATCH_COMPONENT_SERVICE_IMAGE_DIGEST"] != "sha256:service" || serviceApp.HelmGroupValues != "" {
		t.Fatalf("unexpected hook environment or service values: %#v %#v", uiApp.HookEnvironment, serviceApp)
	}
	if serviceApp.PreDeployHook != "echo build service" || uiApp.PostDeployHook != "echo publish ui" || serviceApp.HookEnvironment["DISPATCH_EVENT_REPOSITORY"] != "org/service" || uiApp.HookEnvironment["DISPATCH_EVENT_COMPONENT_ALIAS"] != "ui" {
		t.Fatalf("event hooks and context were not applied to components: service=%#v ui=%#v", serviceApp, uiApp)
	}
	if serviceApp.HookEnvironment[core.SecretEnvironmentKey(registrySecret.ID, registrySecret.EnvironmentVariable)] != registrySecret.EncryptedValue {
		t.Fatalf("component hook credential was not attached: %#v", serviceApp.HookEnvironment)
	}
	uiEvent := core.IncomingEvent{Provider: core.EventProviderGitHub, DeliveryID: "ui-delivery-1", Kind: core.EventKindPullRequestComment,
		Action: "created", Repository: "org/ui", PullRequestNumber: 22, TrustedActor: true, Command: "/preview", ReceivedAt: now}
	uiRuns, err := service.Process(ctx, uiEvent)
	if err != nil || len(uiRuns) != 1 {
		t.Fatalf("start independent UI run: %#v err=%v", uiRuns, err)
	}
	independent := waitForGroupState(t, data, uiRuns[0].ID, core.PreviewGroupReady)

	executor.mu.Lock()
	executor.failSHA = "ui-pr-sha-w"
	executor.mu.Unlock()
	event.DeliveryID, event.Arguments = "delivery-2", "with ui=#22"
	if _, err := service.Process(ctx, event); err != nil {
		t.Fatal(err)
	}
	updated := waitForAttempt(t, data, first.ID, 2)
	if updated.State != core.PreviewGroupReady || sourceForAlias(updated.Sources, "ui").PullRequest != 0 || !strings.Contains(updated.Message, "restored") {
		t.Fatalf("failed update did not restore the prior source set: %#v", updated)
	}
	stillIndependent, err := data.GetPreviewGroupRun(ctx, independent.ID)
	if err != nil || stillIndependent.State != core.PreviewGroupReady {
		t.Fatalf("failed merge changed the independent run: %#v err=%v", stillIndependent, err)
	}
	executor.mu.Lock()
	executor.failSHA = ""
	executor.mu.Unlock()
	event.DeliveryID = "delivery-3"
	if _, err := service.Process(ctx, event); err != nil {
		t.Fatal(err)
	}
	updated = waitForAttempt(t, data, first.ID, 3)
	if updated.State != core.PreviewGroupReady || updated.EntrypointURL != stableURL {
		t.Fatalf("linked update changed the stable preview: %#v", updated)
	}
	if sourceForAlias(updated.Sources, "ui").PullRequest != 22 {
		t.Fatalf("UI pull request was not attached: %#v", updated.Sources)
	}
	waitForGroupState(t, data, independent.ID, core.PreviewGroupClosed)
	notifier.mu.Lock()
	for _, key := range []string{"org/service#11", "org/ui#22"} {
		body := notifier.bodies[key]
		if !strings.Contains(body, "Full stack") || !strings.Contains(body, "with ui=#123") || !strings.Contains(body, stableURL) {
			notifier.mu.Unlock()
			t.Fatalf("combined status missing for %s: %q", key, body)
		}
	}
	notifier.mu.Unlock()

	closeEvent := core.IncomingEvent{Provider: core.EventProviderGitHub, Kind: core.EventKindPullRequest, Action: "closed", Repository: "org/service", PullRequestNumber: 11}
	if _, err := service.Process(ctx, closeEvent); err != nil {
		t.Fatal(err)
	}
	retained, err := data.GetPreviewGroupRun(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retained.State == core.PreviewGroupClosed {
		t.Fatal("group closed while a linked pull request remained open")
	}
	closeEvent.Repository, closeEvent.PullRequestNumber = "org/ui", 22
	if _, err := service.Process(ctx, closeEvent); err != nil {
		t.Fatal(err)
	}
	waitForGroupState(t, data, first.ID, core.PreviewGroupClosed)
	if err := data.DeletePreviewGroup(ctx, group.ID); err != nil {
		t.Fatalf("closed group should be deletable: %v", err)
	}
}

func waitForGroupState(t *testing.T, data store.Store, id string, state core.PreviewGroupState) core.PreviewGroupRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := data.GetPreviewGroupRun(context.Background(), id)
		if err == nil && run.State == state {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	run, err := data.GetPreviewGroupRun(context.Background(), id)
	t.Fatalf("group did not reach %s: %#v err=%v", state, run, err)
	return run
}

func waitForAttempt(t *testing.T, data store.Store, id string, attempt int) core.PreviewGroupRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := data.GetPreviewGroupRun(context.Background(), id)
		if err == nil && run.Attempt >= attempt && (run.State == core.PreviewGroupReady || run.State == core.PreviewGroupDegraded || run.State == core.PreviewGroupFailed) {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	run, err := data.GetPreviewGroupRun(context.Background(), id)
	t.Fatalf("group did not finish attempt %d: %#v err=%v", attempt, run, err)
	return run
}

func TestValidationRejectsCyclesAndOverlappingCommands(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = data.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now})
	_ = data.CreateServer(ctx, core.Server{ID: "s", Name: "s", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/tmp/k"}, CreatedAt: now})
	for _, id := range []string{"a", "b"} {
		_ = data.CreateApp(ctx, core.App{ID: id, ProjectID: "p", ServerID: "s", Name: id, BuildType: core.BuildTypeHelm, HelmChart: "oci://chart/" + id, CreatedAt: now})
	}
	group := core.PreviewGroup{ID: "g", Name: "g", Command: "/preview", Enabled: true, Components: []core.PreviewGroupComponent{
		{ID: "ca", AppID: "a", Alias: "a", Repository: "org/a", DefaultBranch: "main", Entrypoint: true, DependsOn: []string{"b"}},
		{ID: "cb", AppID: "b", Alias: "b", Repository: "org/b", DefaultBranch: "main", DependsOn: []string{"a"}},
	}}
	if err := Validate(ctx, data, &group, "/preview"); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
	group.Components[0].DependsOn, group.Components[1].DependsOn = nil, []string{"a"}
	group.CreatedAt, group.UpdatedAt = now, now
	if err := Validate(ctx, data, &group, "/preview"); err != nil {
		t.Fatal(err)
	}
	if err := data.CreatePreviewGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	overlap := group
	overlap.ID, overlap.Name = "g2", "g2"
	for index := range overlap.Components {
		overlap.Components[index].ID += "2"
	}
	if err := Validate(ctx, data, &overlap, "/preview"); err != store.ErrPreviewGroupOverlap {
		t.Fatalf("expected overlap error, got %v", err)
	}
}

func TestLinkedReferenceParsingSupportsAliasShortAndFullRepository(t *testing.T) {
	group := core.PreviewGroup{Components: []core.PreviewGroupComponent{
		{Alias: "service", Repository: "team/service-api"},
		{Alias: "ui", Repository: "team/web-ui"},
	}}
	for key, alias := range map[string]string{"ui": "ui", "web-ui": "ui", "team/service-api": "service"} {
		component, err := resolveComponentKey(group, key)
		if err != nil || component.Alias != alias {
			t.Fatalf("resolve %q: %#v err=%v", key, component, err)
		}
	}
	links, err := parseLinks("with ui=#12, team/service-api=#34")
	if err != nil || len(links) != 2 || links[0].number != 12 || links[1].number != 34 {
		t.Fatalf("unexpected links: %#v err=%v", links, err)
	}
}

func TestPreviewGroupsAreScopedToGitHubConnection(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_ = data.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now})
	_ = data.CreateServer(ctx, core.Server{ID: "s", Name: "s", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/tmp/k"}, CreatedAt: now})
	_ = data.CreateApp(ctx, core.App{ID: "a", ProjectID: "p", ServerID: "s", Name: "a", BuildType: core.BuildTypeHelm, HelmChart: "oci://chart/a", CreatedAt: now})
	for _, id := range []string{"github-a", "github-b"} {
		if err := data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: id, Name: id, WebURL: "https://" + id + ".example.com", APIURL: "https://" + id + ".example.com/api/v3", AppID: 1, InstallationID: 7, WebhookURL: "https://dispatch.example.com/hooks/" + id, EncryptedPrivateKey: "key", EncryptedWebhookSecret: "secret", State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	group := core.PreviewGroup{ID: "group-a", Name: "group-a", GitHubAppID: "github-a", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now,
		Components: []core.PreviewGroupComponent{{ID: "component-a", AppID: "a", Alias: "service", Repository: "platform/service", DefaultBranch: "main", Entrypoint: true}}}
	if err := Validate(ctx, data, &group, "/preview"); err != nil {
		t.Fatal(err)
	}
	if err := data.CreatePreviewGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	other := group
	other.ID, other.Name, other.GitHubAppID = "group-b", "group-b", "github-b"
	other.Components = []core.PreviewGroupComponent{{ID: "component-b", AppID: "a", Alias: "service", Repository: "platform/service", DefaultBranch: "main", Entrypoint: true}}
	if err := Validate(ctx, data, &other, "/preview"); err != nil {
		t.Fatalf("same command and repository should be allowed on another connector: %v", err)
	}
	if err := data.CreatePreviewGroup(ctx, other); err != nil {
		t.Fatal(err)
	}
	matches, err := data.MatchingPreviewGroups(ctx, core.EventProviderGitHub, "platform/service", "/preview", "github-a")
	if err != nil || len(matches) != 1 || matches[0].ID != group.ID {
		t.Fatalf("unexpected connector-scoped matches: %#v err=%v", matches, err)
	}
}

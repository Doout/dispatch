package events

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
)

func TestCompletionWaitsForDeployingStatusPersistence(t *testing.T) {
	data, template := previewFixture(t)
	deployments := deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond})
	lifecycle := DeploymentLifecycle{Store: data, Deployments: deployments, PollEvery: time.Millisecond}
	notifier := &recordingNotifier{}
	service := New(data, nil, lifecycle, notifier)

	result, err := service.Process(context.Background(), previewCommentEvent("delivery-fast"))
	if err != nil || len(result.Previews) != 1 {
		t.Fatalf("unexpected preview start: result=%#v err=%v", result, err)
	}
	preview := waitForPreviewState(t, data, result.Previews[0].ID, core.PreviewReady)
	if preview.TemplateAppID != template.ID || preview.StatusCommentID != "status-1" {
		t.Fatalf("unexpected completed preview: %#v", preview)
	}
	states, commentIDs := notifier.snapshot()
	if len(states) != 2 || states[0] != core.PreviewDeploying || states[1] != core.PreviewReady {
		t.Fatalf("expected ordered deploying/ready notifications, got %v", states)
	}
	if commentIDs[0] != "" || commentIDs[1] != "status-1" {
		t.Fatalf("completion must update the persisted status comment, got IDs %v", commentIDs)
	}
}

func TestCompletionPersistsTerminalStateWhenNotifierFails(t *testing.T) {
	data, _ := previewFixture(t)
	deployments := deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond})
	lifecycle := DeploymentLifecycle{Store: data, Deployments: deployments, PollEvery: time.Millisecond}
	notifier := &recordingNotifier{failAfter: 1}
	service := New(data, nil, lifecycle, notifier)
	result, err := service.Process(context.Background(), previewCommentEvent("delivery-notifier-failure"))
	if err != nil {
		t.Fatal(err)
	}
	preview := waitForPreviewState(t, data, result.Previews[0].ID, core.PreviewReady)
	if preview.StatusCommentID != "status-1" {
		t.Fatalf("terminal persistence must retain initial status comment: %#v", preview)
	}
	deadline := time.Now().Add(2 * time.Second)
	for notifier.count() < 4 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	states, _ := notifier.snapshot()
	if len(states) != 4 {
		t.Fatalf("expected one initial notification and three bounded completion attempts, got %v", states)
	}
}

func TestDuplicateDeliveryRetriesFailedStart(t *testing.T) {
	data, _ := previewFixture(t)
	lifecycle := &controlledLifecycle{store: data, failStarts: 1}
	service := New(data, nil, lifecycle, nil)
	event := previewCommentEvent("delivery-retry-start")

	result, err := service.Process(context.Background(), event)
	if err == nil || len(result.Previews) != 1 {
		t.Fatalf("expected first start to fail retryably: result=%#v err=%v", result, err)
	}
	preview, err := data.GetPreviewEnvironment(context.Background(), result.Previews[0].ID)
	if err != nil || preview.State != core.PreviewRequested {
		t.Fatalf("failed start must return to requested: preview=%#v err=%v", preview, err)
	}

	result, err = service.Process(context.Background(), event)
	if err != nil || !result.Duplicate || result.Previews[0].State != core.PreviewDeploying {
		t.Fatalf("duplicate delivery must resume requested work: result=%#v err=%v", result, err)
	}
	if lifecycle.startCount() != 2 {
		t.Fatalf("expected two start attempts, got %d", lifecycle.startCount())
	}
}

func TestDuplicateCloseRetriesCleanupFailure(t *testing.T) {
	data, _ := previewFixture(t)
	lifecycle := &controlledLifecycle{store: data, failCleanups: 1}
	service := New(data, nil, lifecycle, nil)
	started, err := service.Process(context.Background(), previewCommentEvent("delivery-cleanup-start"))
	if err != nil {
		t.Fatal(err)
	}
	closeEvent := previewClose("delivery-cleanup-close")
	closed, err := service.Process(context.Background(), closeEvent)
	if err == nil || closed.Previews[0].State != core.PreviewCleanupRequested {
		t.Fatalf("failed cleanup must remain retryable: result=%#v err=%v", closed, err)
	}
	closed, err = service.Process(context.Background(), closeEvent)
	if err != nil || !closed.Duplicate || closed.Previews[0].State != core.PreviewClosed {
		t.Fatalf("duplicate close must retry cleanup: result=%#v err=%v", closed, err)
	}
	if lifecycle.cleanupCount() != 2 {
		t.Fatalf("expected two cleanup attempts, got %d", lifecycle.cleanupCount())
	}
	if _, err := data.GetApp(context.Background(), started.Previews[0].AppID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("successful cleanup must delete generated app, got %v", err)
	}
}

func TestCloseDuringStartNeverCleansTemplateOrReopensPreview(t *testing.T) {
	data, template := previewFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	lifecycle := &controlledLifecycle{store: data, startEntered: entered, releaseStart: release}
	service := New(data, nil, lifecycle, nil)
	commentDone := make(chan error, 1)
	go func() {
		_, err := service.Process(context.Background(), previewCommentEvent("delivery-race-start"))
		commentDone <- err
	}()
	<-entered
	closeDone := make(chan error, 1)
	go func() {
		_, err := service.Process(context.Background(), previewClose("delivery-race-close"))
		closeDone <- err
	}()
	close(release)
	if err := <-commentDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	previews, err := data.ListPreviewEnvironments(context.Background(), template.ID)
	if err != nil || len(previews) != 1 || previews[0].State != core.PreviewClosed {
		t.Fatalf("close must win start race: previews=%#v err=%v", previews, err)
	}
	if _, err := data.GetApp(context.Background(), template.ID); err != nil {
		t.Fatalf("template application must survive preview cleanup: %v", err)
	}
}

func TestCloseBeforeCommandPreventsPreviewCreation(t *testing.T) {
	data, _ := previewFixture(t)
	service := New(data, nil, &controlledLifecycle{store: data}, nil)
	closed, err := service.Process(context.Background(), previewClose("delivery-close-first"))
	if err != nil || !closed.Ignored {
		t.Fatalf("unexpected close result: %#v err=%v", closed, err)
	}
	comment, err := service.Process(context.Background(), previewCommentEvent("delivery-comment-after-close"))
	if err != nil || !comment.Ignored || len(comment.Previews) != 0 {
		t.Fatalf("closed pull request must not start a preview: %#v err=%v", comment, err)
	}
}

func TestCleanupAbandonsDeploymentMissingAfterRestart(t *testing.T) {
	data, template := previewFixture(t)
	now := time.Now().UTC()
	instance := template
	instance.ID, instance.Name, instance.State, instance.CreatedAt = "orphan-instance", "orphan-instance", "preview", now
	if err := data.CreateApp(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-time.Minute)
	deployment := core.Deployment{ID: "orphan-deployment", AppID: instance.ID, CommitSHA: "abc123", SpecDigest: instance.SpecDigest(), State: core.DeploymentBuilding, Message: "lost worker", CreatedAt: now.Add(-time.Hour), LeaseUntil: &expired}
	if err := data.CreateDeployment(context.Background(), deployment); err != nil {
		t.Fatal(err)
	}
	lifecycle := DeploymentLifecycle{Store: data, Deployments: deploy.NewService(data, deploy.SimulationExecutor{})}
	preview := core.PreviewEnvironment{AppID: instance.ID}
	if err := lifecycle.CleanupPreview(context.Background(), preview); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetApp(context.Background(), instance.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale deployment must not block preview app deletion: %v", err)
	}
}

type recordingNotifier struct {
	mu         sync.Mutex
	states     []core.PreviewState
	commentIDs []string
	failAfter  int
}

func (n *recordingNotifier) UpdatePreview(_ context.Context, notification Notification) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.states = append(n.states, notification.State)
	n.commentIDs = append(n.commentIDs, notification.Preview.StatusCommentID)
	if n.failAfter > 0 && len(n.states) > n.failAfter {
		return "", errors.New("temporary notification failure")
	}
	if notification.Preview.StatusCommentID != "" {
		return notification.Preview.StatusCommentID, nil
	}
	return "status-1", nil
}

func (n *recordingNotifier) snapshot() ([]core.PreviewState, []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]core.PreviewState(nil), n.states...), append([]string(nil), n.commentIDs...)
}

func (n *recordingNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.states)
}

type controlledLifecycle struct {
	store        *store.SQLStore
	mu           sync.Mutex
	starts       int
	cleanups     int
	failStarts   int
	failCleanups int
	startEntered chan struct{}
	releaseStart chan struct{}
}

func (l *controlledLifecycle) StartPreview(ctx context.Context, preview core.PreviewEnvironment) (StartResult, error) {
	l.mu.Lock()
	l.starts++
	if l.failStarts > 0 {
		l.failStarts--
		l.mu.Unlock()
		return StartResult{}, errors.New("temporary start failure")
	}
	l.mu.Unlock()
	template, err := l.store.GetApp(ctx, preview.TemplateAppID)
	if err != nil {
		return StartResult{}, err
	}
	instance := template
	instance.ID, instance.Name, instance.State, instance.CreatedAt = ulid.Make().String(), template.Name+"-instance", "preview", time.Now().UTC()
	if err := l.store.CreateApp(ctx, instance); err != nil {
		return StartResult{}, err
	}
	if l.startEntered != nil {
		close(l.startEntered)
		<-l.releaseStart
	}
	return StartResult{AppID: instance.ID, DeploymentID: "deployment-1", URL: "https://preview.example.test", Message: "Preview deployment started"}, nil
}

func (l *controlledLifecycle) ResumePreview(core.PreviewEnvironment) <-chan Completion { return nil }

func (l *controlledLifecycle) CleanupPreview(ctx context.Context, preview core.PreviewEnvironment) error {
	l.mu.Lock()
	l.cleanups++
	if l.failCleanups > 0 {
		l.failCleanups--
		l.mu.Unlock()
		return errors.New("temporary cleanup failure")
	}
	l.mu.Unlock()
	return l.store.DeleteApp(ctx, preview.AppID)
}

func (l *controlledLifecycle) startCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.starts
}

func (l *controlledLifecycle) cleanupCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.cleanups
}

func previewFixture(t *testing.T) (*store.SQLStore, core.App) {
	t.Helper()
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
	project := core.Project{ID: "project-events", Name: "Events", CreatedAt: now}
	server := core.Server{ID: "server-events", Name: "Events", Address: "local", Runtime: "docker", State: "ready", AgentMode: "local", CreatedAt: now}
	app := core.App{ID: "template-events", ProjectID: project.ID, ServerID: server.ID, Name: "Preview", SourceRepo: "https://example.test/acme/checkout.git", Branch: "main", BuildType: core.BuildTypeDockerfile, ContextPath: ".", DockerfilePath: "Dockerfile", Domain: "pr-{pr}.example.test", State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	trigger := core.EventTrigger{ID: "trigger-events", AppID: app.ID, Provider: core.EventProviderGitHub, Repository: "acme/checkout", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if _, _, err := data.CreateEventTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	return data, app
}

func previewCommentEvent(delivery string) core.IncomingEvent {
	return core.IncomingEvent{ID: ulid.Make().String(), Provider: core.EventProviderGitHub, DeliveryID: delivery,
		Kind: core.EventKindPullRequestComment, Action: "created", Repository: "acme/checkout", PullRequestNumber: 42,
		HeadRef: "feature/cart", HeadSHA: "abc123", BaseRef: "main", Actor: "octo", ActorAssociation: "MEMBER",
		TrustedActor: true, Command: "/preview", SourceCommentID: "501", ReceivedAt: time.Now().UTC()}
}

func previewClose(delivery string) core.IncomingEvent {
	return core.IncomingEvent{ID: ulid.Make().String(), Provider: core.EventProviderGitHub, DeliveryID: delivery,
		Kind: core.EventKindPullRequest, Action: "closed", Repository: "acme/checkout", PullRequestNumber: 42,
		HeadRef: "feature/cart", HeadSHA: "abc123", BaseRef: "main", Actor: "octo", ReceivedAt: time.Now().UTC()}
}

func waitForPreviewState(t *testing.T, data *store.SQLStore, id string, expected core.PreviewState) core.PreviewEnvironment {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		preview, err := data.GetPreviewEnvironment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if preview.State == expected {
			return preview
		}
		time.Sleep(5 * time.Millisecond)
	}
	preview, _ := data.GetPreviewEnvironment(context.Background(), id)
	t.Fatalf("timed out waiting for %s, last preview %#v", expected, preview)
	return core.PreviewEnvironment{}
}

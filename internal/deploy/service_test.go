package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestApplicationTemplateCannotStartDirectDeployment(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	project := core.Project{ID: "project", Name: "Project", CreatedAt: time.Now().UTC()}
	server := core.Server{ID: "server", Name: "Docker", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: time.Now().UTC()}
	app := core.App{ID: "template", ProjectID: project.ID, ServerID: server.ID, Name: "Template", BuildType: core.BuildTypeCompose, ComposeContent: "services: {}", Template: true, State: "template", CreatedAt: time.Now().UTC()}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, SimulationExecutor{})
	if _, err := service.Start(ctx, app.ID, "inline"); !errors.Is(err, ErrApplicationTemplate) {
		t.Fatalf("expected template deployment rejection, got %v", err)
	}
	deployments, err := data.ListDeployments(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(deployments) != 0 {
		t.Fatalf("template deployment must not create work, got %#v", deployments)
	}
}

func TestSimulationDeploymentCompletesWithEvidence(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
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
	apps, err := data.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	appID := ""
	for _, app := range apps {
		active, err := data.ActiveDeploymentForApp(ctx, app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if active == nil {
			appID = app.ID
			break
		}
	}
	if appID == "" {
		t.Fatal("demo seed did not include an app without active work")
	}

	service := NewService(data, SimulationExecutor{Delay: time.Millisecond})
	created, err := service.Start(ctx, appID, "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := data.GetDeployment(ctx, created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State.Terminal() {
			if current.State != "succeeded" {
				t.Fatalf("deployment ended as %q: %s", current.State, current.Message)
			}
			logs, err := data.ListDeploymentLogs(ctx, created.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(logs) < 7 {
				t.Fatalf("expected full transition evidence, got %d entries", len(logs))
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("deployment did not complete before the deadline")
}

func TestWithinRejectsRepositoryEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := within(root, "../outside"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
	if path, err := within(root, "Dockerfile"); err != nil || path == "" {
		t.Fatalf("expected a contained path, got %q, %v", path, err)
	}
}

func TestDockerExecutorDeploysPastedComposeWithoutGit(t *testing.T) {
	content := "services:\n  app:\n    image: ghcr.io/example/app:latest"
	commands := [][]string{}
	executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, _ io.Writer, name string, args ...string) error {
		if name == "git" {
			t.Fatal("pasted Compose deployment must not invoke Git")
		}
		commands = append(commands, append([]string{name}, args...))
		for index, arg := range args {
			if arg != "-f" || index+1 >= len(args) {
				continue
			}
			stored, err := os.ReadFile(args[index+1])
			if err != nil {
				t.Fatal(err)
			}
			if string(stored) != content {
				t.Fatalf("unexpected materialized Compose definition: %q", stored)
			}
		}
		return nil
	}}
	states := []core.DeploymentState{}
	err := executor.Deploy(context.Background(), core.Deployment{ID: "deployment-1"}, core.App{
		ID: "app-1", Name: "Pasted", BuildType: core.BuildTypeCompose, ComposeContent: content,
	}, core.Server{Name: "local-docker", Address: "local"}, func(state core.DeploymentState, _ string) error {
		states = append(states, state)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || commands[0][0] != "docker" || commands[1][0] != "docker" {
		t.Fatalf("expected Compose validation and apply commands, got %#v", commands)
	}
	wantStates := []core.DeploymentState{core.DeploymentFetching, core.DeploymentBuilding, core.DeploymentStarting, core.DeploymentChecking, core.DeploymentRouting}
	if !reflect.DeepEqual(states, wantStates) {
		t.Fatalf("unexpected deployment states: %#v", states)
	}
}

func TestDockerExecutorCleansUpPastedCompose(t *testing.T) {
	content := "services:\n  app:\n    image: example/app:latest"
	var command []string
	executor := DockerExecutor{run: func(_ context.Context, _ io.Reader, _ io.Writer, name string, args ...string) error {
		if name == "git" {
			t.Fatal("pasted Compose cleanup must not invoke Git")
		}
		command = append([]string{name}, args...)
		for index, arg := range args {
			if arg != "-f" || index+1 >= len(args) {
				continue
			}
			stored, err := os.ReadFile(args[index+1])
			if err != nil {
				t.Fatal(err)
			}
			if string(stored) != content {
				t.Fatalf("unexpected materialized Compose definition: %q", stored)
			}
		}
		return nil
	}}
	states := []core.DeploymentState{}
	err := executor.Cleanup(context.Background(), core.App{
		ID: "app-1", Name: "Pasted", BuildType: core.BuildTypeCompose, ComposeContent: content,
	}, core.Server{Name: "local-docker", Address: "local"}, func(state core.DeploymentState, _ string) error {
		states = append(states, state)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command) < 2 || command[0] != "docker" || command[len(command)-2] != "down" || command[len(command)-1] != "--remove-orphans" {
		t.Fatalf("unexpected cleanup command: %#v", command)
	}
	if !reflect.DeepEqual(states, []core.DeploymentState{core.DeploymentStarting, core.DeploymentSucceeded}) {
		t.Fatalf("unexpected cleanup states: %#v", states)
	}
}

func TestServiceLocksApplicationsIndependently(t *testing.T) {
	service := NewService(nil, nil)
	unlockFirst := service.lockApp("app-1")
	defer unlockFirst()

	lockedSecond := make(chan func(), 1)
	go func() { lockedSecond <- service.lockApp("app-2") }()
	select {
	case unlockSecond := <-lockedSecond:
		unlockSecond()
	case <-time.After(time.Second):
		t.Fatal("an operation for one application blocked an unrelated application")
	}
}

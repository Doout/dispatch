package deploy

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/runtimecontract/conformance"
	"github.com/doout/dispatch/internal/store"
)

func TestRuntimeConformance(t *testing.T) {
	for _, driver := range []string{"simulation", "docker"} {
		t.Run(driver, func(t *testing.T) {
			conformance.Run(t, func(t *testing.T) conformance.Fixture {
				mutations, resources := 0, 0
				app := core.App{ID: "conformance", Name: "Conformance", BuildType: core.BuildTypeCompose, ComposeContent: "services:\n  app:\n    image: busybox:1.37\n"}
				server := core.Server{ID: "local", Address: "local", Runtime: core.ServerRuntimeDocker}
				var executor Executor = SimulationExecutor{Delay: time.Nanosecond}
				if driver == "docker" {
					executor = DockerExecutor{run: func(_ context.Context, _ io.Reader, output io.Writer, name string, args ...string) error {
						if name != "docker" {
							t.Fatalf("unexpected command: %s", name)
						}
						if len(args) > 0 && args[0] == "ps" && resources > 0 {
							_, _ = io.WriteString(output, "container")
						}
						if len(args) > 0 && args[0] == "inspect" {
							if strings.Contains(strings.Join(args, " "), ".State.Running") {
								_, _ = io.WriteString(output, `{"running":true,"health":"none"}`)
							} else {
								_, _ = io.WriteString(output, `{"service":"app","networks":{}}`)
							}
						}
						for _, arg := range args {
							if arg == "up" {
								mutations++
								resources = 1
							}
							if arg == "down" {
								mutations++
								resources = 0
							}
						}
						return nil
					}}
				}
				runtime := RuntimeExecutor{Default: executor}
				return conformance.Fixture{
					Manifest: runtime.RuntimeCapabilities(app, server),
					Execute: func(ctx context.Context, op runtimecontract.Operation) error {
						err := runtime.Execute(ctx, op, core.Deployment{ID: "operation", CommitSHA: "inline"}, app, server, func(core.DeploymentState, string) error { return nil })
						if driver == "simulation" && err == nil {
							mutations++
							if op == runtimecontract.Deploy {
								resources = 1
							} else {
								resources = 0
							}
						}
						return err
					},
					MutationCount: func() int { return mutations }, ResourceCount: func() int { return resources },
				}
			})
		})
	}
}

func TestRuntimeRejectsUnsupportedTargetBeforeCredentialResolution(t *testing.T) {
	executor := SourceAuthExecutor{Next: RuntimeExecutor{Default: DockerExecutor{}}}
	err := executor.Deploy(context.Background(), core.Deployment{}, core.App{BuildType: core.BuildTypeDockerfile, SourceAuthType: SourceAuthGitHubToken, SourceCredentialID: "must-not-resolve"}, core.Server{Address: "remote.example.test"}, nil)
	var classified *runtimecontract.Error
	if !errors.As(err, &classified) || classified.Code != runtimecontract.Unsupported {
		t.Fatalf("expected rejection before credential resolution, got %v", err)
	}
}

func fixtureGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestDockerExecutesAcceptedCommitAfterBranchMoves(t *testing.T) {
	repo := t.TempDir()
	serviceFixtureRepo(t, repo, map[string]string{"Dockerfile": "FROM scratch\n", "revision": "accepted"})
	accepted := fixtureGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "revision"), []byte("newer"), 0600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, repo, "add", "revision")
	fixtureGit(t, repo, "commit", "-m", "Advance branch")
	checked := false
	executor := DockerExecutor{run: func(ctx context.Context, stdin io.Reader, output io.Writer, name string, args ...string) error {
		if name == "git" {
			return command(ctx, stdin, output, name, args...)
		}
		if len(args) > 0 && args[0] == "inspect" && strings.Contains(strings.Join(args, " "), ".State.Running") {
			_, _ = io.WriteString(output, `{"running":true,"health":"none"}`)
		}
		if len(args) > 0 && args[0] == "build" {
			workspace := args[len(args)-1]
			content, err := os.ReadFile(filepath.Join(workspace, "revision"))
			if err != nil || string(content) != "accepted" {
				t.Fatalf("built wrong source %q: %v", content, err)
			}
			if got := fixtureGit(t, workspace, "rev-parse", "HEAD"); got != accepted {
				t.Fatalf("built %s, accepted %s", got, accepted)
			}
			checked = true
		}
		return nil
	}}
	app := core.App{ID: "app", Name: "App", BuildType: core.BuildTypeDockerfile, SourceRepo: "file://" + repo, Branch: "main", ContextPath: ".", DockerfilePath: "Dockerfile"}
	if err := executor.Deploy(context.Background(), core.Deployment{ID: "run", CommitSHA: accepted}, app, core.Server{Address: "local"}, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("build was never reached")
	}
}

func TestSourceResolutionIsSavedBeforeDeploymentAcceptance(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	serviceFixtureRepo(t, repo, map[string]string{"Dockerfile": "FROM scratch\n"})
	accepted := fixtureGit(t, repo, "rev-parse", "HEAD")
	data, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	project := core.Project{ID: "project", Name: "Project", CreatedAt: time.Now()}
	server := core.Server{ID: "server", Name: "Server", Address: "local", Runtime: core.ServerRuntimeDocker, State: "ready", CreatedAt: time.Now()}
	app := core.App{ID: "app", Name: "App", ProjectID: project.ID, ServerID: server.ID, SourceRepo: "file://" + repo, Branch: "main", BuildType: core.BuildTypeDockerfile, CreatedAt: time.Now()}
	for _, err := range []error{data.CreateProject(ctx, project), data.CreateServer(ctx, server), data.CreateApp(ctx, app)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	svc := NewService(data, SimulationExecutor{Delay: time.Millisecond})
	svc.ConfigureSourceResolution(SourceAuthExecutor{})
	run, err := svc.Start(ctx, app.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if run.CommitSHA != accepted {
		t.Fatalf("accepted mutable revision %q, want %q", run.CommitSHA, accepted)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		saved, err := data.GetDeployment(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.State.Terminal() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("deployment did not finish")
}

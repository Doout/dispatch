package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type Progress func(core.DeploymentState, string) error

type Executor interface {
	Deploy(context.Context, core.Deployment, core.App, core.Server, Progress) error
}

type CleanupExecutor interface {
	Cleanup(context.Context, core.App, core.Server, Progress) error
}

type RuntimeExecutor struct {
	Default Executor
	Helm    Executor
}

func (e RuntimeExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	executor := e.Default
	if app.BuildType == core.BuildTypeHelm {
		executor = e.Helm
	}
	if executor == nil {
		return fmt.Errorf("no executor configured for %s applications", app.BuildType)
	}
	return executor.Deploy(ctx, deployment, app, server, progress)
}

func (e RuntimeExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	executor := e.Default
	if app.BuildType == core.BuildTypeHelm {
		executor = e.Helm
	}
	cleaner, ok := executor.(CleanupExecutor)
	if !ok {
		return ErrCleanupUnsupported
	}
	return cleaner.Cleanup(ctx, app, server, progress)
}

var ErrCleanupUnsupported = errors.New("application executor does not support cleanup")

type SimulationExecutor struct {
	Delay time.Duration
}

func (e SimulationExecutor) Deploy(ctx context.Context, _ core.Deployment, app core.App, server core.Server, progress Progress) error {
	delay := e.Delay
	if delay == 0 {
		delay = 350 * time.Millisecond
	}
	sourceMessage := "Resolved source revision from " + app.SourceRepo
	if app.BuildType == core.BuildTypeHelm {
		sourceMessage = "Resolved Helm chart " + app.HelmChart
	} else if app.ComposeContent != "" {
		sourceMessage = "Loaded saved Compose definition"
	}
	steps := []struct {
		state core.DeploymentState
		text  string
	}{
		{core.DeploymentFetching, sourceMessage},
		{core.DeploymentBuilding, buildMessage(app)},
		{core.DeploymentStarting, "Started candidate revision on " + server.Name},
		{core.DeploymentChecking, "Readiness checks passed"},
		{core.DeploymentRouting, routeMessage(app)},
	}
	for _, step := range steps {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if err := progress(step.state, step.text); err != nil {
			return err
		}
	}
	return nil
}

func (SimulationExecutor) Cleanup(_ context.Context, app core.App, _ core.Server, progress Progress) error {
	if err := progress(core.DeploymentStarting, "Removing simulated resources for "+app.Name); err != nil {
		return err
	}
	return progress(core.DeploymentSucceeded, "Simulated resources removed")
}

func buildMessage(app core.App) string {
	if app.BuildType == core.BuildTypeHelm {
		return "Prepared Helm release inputs"
	}
	return "Built " + string(app.BuildType) + " application artifact"
}

func routeMessage(app core.App) string {
	if app.Domain == "" {
		return "Deployment made available on its configured container port"
	}
	return "Prepared route for " + app.Domain
}

type commandFunc func(context.Context, io.Reader, io.Writer, string, ...string) error

type DockerExecutor struct {
	run commandFunc
}

var safeID = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func (e DockerExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	return e.deploy(ctx, deployment, app, server, progress)
}

func (e DockerExecutor) deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	if server.Address != "local" && server.Address != "127.0.0.1" && server.Address != "localhost" {
		return errors.New("live Docker execution currently requires a local enrolled server")
	}
	directCompose := app.BuildType == core.BuildTypeCompose && strings.TrimSpace(app.ComposeContent) != ""
	if !directCompose && !validGitSourceForExecution(app) {
		return errors.New("live Docker execution requires an HTTPS or file repository, or an SSH repository with a configured key")
	}
	workspace, err := os.MkdirTemp("", "dispatch-deploy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	composePath := ""
	if directCompose {
		if err := progress(core.DeploymentFetching, "Loading saved Compose definition"); err != nil {
			return err
		}
		composePath = filepath.Join(workspace, "compose.yml")
		if err := os.WriteFile(composePath, []byte(app.ComposeContent), 0o600); err != nil {
			return fmt.Errorf("write compose definition: %w", err)
		}
	} else {
		if err := progress(core.DeploymentFetching, "Fetching exact repository branch "+app.Branch); err != nil {
			return err
		}
		if err := e.gitCommand(ctx, app, "clone", "--depth", "1", "--branch", app.Branch, app.SourceRepo, workspace); err != nil {
			return fmt.Errorf("fetch source: %w", err)
		}
	}

	name := dockerResourceName(app.ID)
	if app.BuildType == core.BuildTypeCompose {
		if err := progress(core.DeploymentBuilding, "Validating Compose application"); err != nil {
			return err
		}
		if composePath == "" {
			var err error
			composePath, err = within(workspace, app.ComposePath)
			if err != nil {
				return fmt.Errorf("compose path: %w", err)
			}
		}
		if err := e.commandWithOutput(ctx, "docker", "compose", "-p", name, "-f", composePath, "config", "--quiet"); err != nil {
			return fmt.Errorf("validate compose: %w", err)
		}
		if err := progress(core.DeploymentStarting, "Applying Compose project on "+server.Name); err != nil {
			return err
		}
		if err := e.commandWithOutput(ctx, "docker", "compose", "-p", name, "-f", composePath, "up", "-d", "--build", "--remove-orphans"); err != nil {
			return fmt.Errorf("deploy compose: %w", err)
		}
	} else {
		contextPath, err := within(workspace, app.ContextPath)
		if err != nil {
			return fmt.Errorf("context path: %w", err)
		}
		dockerfilePath, err := within(workspace, app.DockerfilePath)
		if err != nil {
			return fmt.Errorf("dockerfile path: %w", err)
		}
		image := "dispatch/" + safeID.ReplaceAllString(strings.ToLower(app.Name), "-") + ":" + strings.ToLower(deployment.ID)
		if err := progress(core.DeploymentBuilding, "Building immutable image "+image); err != nil {
			return err
		}
		if err := e.command(ctx, nil, io.Discard, "docker", "build", "--pull", "-f", dockerfilePath, "-t", image, contextPath); err != nil {
			return fmt.Errorf("docker build: %w", err)
		}
		if err := progress(core.DeploymentStarting, "Starting candidate container on "+server.Name); err != nil {
			return err
		}
		_ = e.command(ctx, nil, io.Discard, "docker", "rm", "-f", name)
		args := []string{"run", "-d", "--name", name, "--label", "dispatch.app=" + app.ID, "--label", "dispatch.deployment=" + deployment.ID}
		if app.ContainerPort > 0 {
			args = append(args, "-p", fmt.Sprintf("%d:%d", app.ContainerPort, app.ContainerPort))
		}
		args = append(args, image)
		if err := e.command(ctx, nil, io.Discard, "docker", args...); err != nil {
			return fmt.Errorf("start container: %w", err)
		}
	}
	if err := progress(core.DeploymentChecking, "Docker reports the application running"); err != nil {
		return err
	}
	if err := progress(core.DeploymentRouting, routeMessage(app)); err != nil {
		return err
	}
	return nil
}

func (e DockerExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	if server.Address != "local" && server.Address != "127.0.0.1" && server.Address != "localhost" {
		return errors.New("live Docker cleanup currently requires a local enrolled server")
	}
	name := dockerResourceName(app.ID)
	if err := progress(core.DeploymentStarting, "Removing Docker resources for "+app.Name); err != nil {
		return err
	}
	if app.BuildType == core.BuildTypeCompose {
		workspace, err := os.MkdirTemp("", "dispatch-cleanup-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(workspace)
		composePath := filepath.Join(workspace, "compose.yml")
		if strings.TrimSpace(app.ComposeContent) != "" {
			if err := os.WriteFile(composePath, []byte(app.ComposeContent), 0o600); err != nil {
				return fmt.Errorf("write compose definition: %w", err)
			}
		} else {
			if !validGitSourceForExecution(app) {
				return errors.New("live Docker cleanup requires an HTTPS or file repository, or an SSH repository with a configured key")
			}
			args := []string{"clone", "--depth", "1"}
			if app.Branch != "" {
				args = append(args, "--branch", app.Branch)
			}
			args = append(args, app.SourceRepo, workspace)
			if err := e.gitCommand(ctx, app, args...); err != nil {
				return fmt.Errorf("fetch source for cleanup: %w", err)
			}
			composePath, err = within(workspace, app.ComposePath)
			if err != nil {
				return fmt.Errorf("compose path: %w", err)
			}
		}
		if err := e.commandWithOutput(ctx, "docker", "compose", "-p", name, "-f", composePath, "down", "--remove-orphans"); err != nil {
			return fmt.Errorf("remove Compose project: %w", err)
		}
	} else {
		var output strings.Builder
		if err := e.command(ctx, nil, &output, "docker", "rm", "-f", name); err != nil && !strings.Contains(strings.ToLower(output.String()), "no such container") {
			return fmt.Errorf("remove container: %w", err)
		}
	}
	return progress(core.DeploymentSucceeded, "Docker resources removed")
}

func dockerResourceName(appID string) string {
	return "dispatch-" + safeID.ReplaceAllString(strings.ToLower(appID), "-")
}

func (e DockerExecutor) command(ctx context.Context, stdin io.Reader, output io.Writer, name string, args ...string) error {
	if e.run != nil {
		return e.run(ctx, stdin, output, name, args...)
	}
	return command(ctx, stdin, output, name, args...)
}

func (e DockerExecutor) gitCommand(ctx context.Context, app core.App, args ...string) error {
	if e.run != nil {
		return e.run(ctx, nil, io.Discard, "git", args...)
	}
	return runGitForApp(ctx, app, args...)
}

func validGitSourceForExecution(app core.App) bool {
	if strings.HasPrefix(app.SourceRepo, "https://") || strings.HasPrefix(app.SourceRepo, "file://") {
		return true
	}
	return app.SourceAuthType == SourceAuthSSHKey && (strings.HasPrefix(app.SourceRepo, "ssh://") || strings.Contains(app.SourceRepo, "@"))
}

func (e DockerExecutor) commandWithOutput(ctx context.Context, name string, args ...string) error {
	var output strings.Builder
	err := e.command(ctx, nil, &output, name, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output.String())
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func within(root, requested string) (string, error) {
	path := filepath.Join(root, filepath.Clean(requested))
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return "", errors.New("path escapes repository")
	}
	return path, nil
}

func command(ctx context.Context, stdin io.Reader, output io.Writer, name string, args ...string) error {
	return commandWithEnvironment(ctx, nil, stdin, output, name, args...)
}

func commandWithEnvironment(ctx context.Context, environment []string, stdin io.Reader, output io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if environment != nil {
		cmd.Env = environment
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, output, output
	return cmd.Run()
}

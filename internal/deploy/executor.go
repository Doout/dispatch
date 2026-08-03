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

type SimulationExecutor struct {
	Delay time.Duration
}

func (e SimulationExecutor) Deploy(ctx context.Context, _ core.Deployment, app core.App, server core.Server, progress Progress) error {
	delay := e.Delay
	if delay == 0 {
		delay = 350 * time.Millisecond
	}
	steps := []struct {
		state core.DeploymentState
		text  string
	}{
		{core.DeploymentFetching, "Resolved source revision from " + app.SourceRepo},
		{core.DeploymentBuilding, "Built " + string(app.BuildType) + " application artifact"},
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

func routeMessage(app core.App) string {
	if app.Domain == "" {
		return "Deployment made available on its configured container port"
	}
	return "Prepared route for " + app.Domain
}

type DockerExecutor struct{}

var safeID = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func (DockerExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	if server.Address != "local" && server.Address != "127.0.0.1" && server.Address != "localhost" {
		return errors.New("live Docker execution currently requires a local enrolled server")
	}
	if !strings.HasPrefix(app.SourceRepo, "https://") && !strings.HasPrefix(app.SourceRepo, "file://") {
		return errors.New("live Docker execution requires an HTTPS or file source repository")
	}
	workspace, err := os.MkdirTemp("", "dispatch-deploy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	if err := progress(core.DeploymentFetching, "Fetching exact repository branch "+app.Branch); err != nil {
		return err
	}
	if err := command(ctx, nil, io.Discard, "git", "clone", "--depth", "1", "--branch", app.Branch, app.SourceRepo, workspace); err != nil {
		return fmt.Errorf("fetch source: %w", err)
	}

	name := "dispatch-" + safeID.ReplaceAllString(strings.ToLower(app.ID), "-")
	if app.BuildType == core.BuildTypeCompose {
		if err := progress(core.DeploymentBuilding, "Validating Compose application"); err != nil {
			return err
		}
		composePath, err := within(workspace, app.ComposePath)
		if err != nil {
			return fmt.Errorf("compose path: %w", err)
		}
		if err := command(ctx, nil, io.Discard, "docker", "compose", "-p", name, "-f", composePath, "config", "--quiet"); err != nil {
			return fmt.Errorf("validate compose: %w", err)
		}
		if err := progress(core.DeploymentStarting, "Applying Compose project on "+server.Name); err != nil {
			return err
		}
		if err := command(ctx, nil, io.Discard, "docker", "compose", "-p", name, "-f", composePath, "up", "-d", "--build", "--remove-orphans"); err != nil {
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
		if err := command(ctx, nil, io.Discard, "docker", "build", "--pull", "-f", dockerfilePath, "-t", image, contextPath); err != nil {
			return fmt.Errorf("docker build: %w", err)
		}
		if err := progress(core.DeploymentStarting, "Starting candidate container on "+server.Name); err != nil {
			return err
		}
		_ = command(ctx, nil, io.Discard, "docker", "rm", "-f", name)
		args := []string{"run", "-d", "--name", name, "--label", "dispatch.app=" + app.ID, "--label", "dispatch.deployment=" + deployment.ID}
		if app.ContainerPort > 0 {
			args = append(args, "-p", fmt.Sprintf("%d:%d", app.ContainerPort, app.ContainerPort))
		}
		args = append(args, image)
		if err := command(ctx, nil, io.Discard, "docker", args...); err != nil {
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
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, output, output
	return cmd.Run()
}

package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

type hookRunFunc func(context.Context, string, string, []string) error
type checkoutFunc func(context.Context, core.Deployment, core.App, string) error

// HookExecutor runs trusted application hooks around an underlying deployment.
// Pre and post hooks share a workspace and may add Helm overrides through DISPATCH_VALUES_FILE.
type HookExecutor struct {
	Next    Executor
	Outputs interface {
		UpdateDeploymentOutputs(context.Context, string, map[string]string) error
	}
	runHook  hookRunFunc
	checkout checkoutFunc
}

func (e HookExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
	if e.Next == nil {
		return errors.New("hook executor requires a deployment executor")
	}
	if strings.TrimSpace(app.PreDeployHook) == "" && strings.TrimSpace(app.PostDeployHook) == "" {
		return e.Next.Deploy(ctx, deployment, app, server, progress)
	}
	workspace, err := os.MkdirTemp("", "dispatch-hooks-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	if app.SourceRepo != "" {
		if err := progress(core.DeploymentFetching, "Checking out deployment source for hooks"); err != nil {
			return err
		}
		checkout := e.checkout
		if checkout == nil {
			checkout = checkoutHookSource
		}
		if err := checkout(ctx, deployment, app, workspace); err != nil {
			return fmt.Errorf("prepare hook source: %w", err)
		}
	}
	valuesPath := filepath.Join(workspace, "dispatch-values.yaml")
	outputPath := filepath.Join(workspace, "dispatch-outputs.json")
	environment := hookEnvironment(deployment, app, server, valuesPath, outputPath)
	if app.PreDeployHook != "" {
		if err := progress(core.DeploymentBuilding, "Running pre-deploy hook"); err != nil {
			return err
		}
		if err := e.executeHook(ctx, app.PreDeployHook, workspace, environment); err != nil {
			return fmt.Errorf("pre-deploy hook: %w", err)
		}
		generated, err := os.ReadFile(valuesPath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read generated Helm values: %w", err)
		}
		if len(generated) > maxGeneratedValuesBytes {
			return errors.New("generated Helm values exceed 512 KB")
		}
		if strings.TrimSpace(string(generated)) != "" {
			app.HelmGeneratedValues = string(generated)
		}
	}
	nextProgress := progress
	if app.PreDeployHook != "" {
		nextProgress = func(state core.DeploymentState, message string) error {
			if state == core.DeploymentFetching {
				state = core.DeploymentBuilding
			}
			return progress(state, message)
		}
	}
	if err := e.Next.Deploy(ctx, deployment, app, server, nextProgress); err != nil {
		return err
	}
	if app.PostDeployHook != "" {
		if err := progress(core.DeploymentRouting, "Running post-deploy hook"); err != nil {
			return err
		}
		if err := e.executeHook(ctx, app.PostDeployHook, workspace, environment); err != nil {
			postErr := fmt.Errorf("post-deploy hook: %w", err)
			cleaner, ok := e.Next.(CleanupExecutor)
			if !ok {
				return errors.Join(postErr, ErrCleanupUnsupported)
			}
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
			defer cancel()
			if cleanupErr := cleaner.Cleanup(rollbackCtx, app, server, func(core.DeploymentState, string) error { return nil }); cleanupErr != nil {
				return errors.Join(postErr, fmt.Errorf("rollback deployment: %w", cleanupErr))
			}
			return fmt.Errorf("%w; deployment rolled back", postErr)
		}
		outputs, err := readHookOutputs(outputPath)
		if err != nil {
			return e.rollbackPostHook(ctx, app, server, fmt.Errorf("post-deploy outputs: %w", err))
		}
		if len(outputs) > 0 && e.Outputs != nil {
			if err := e.Outputs.UpdateDeploymentOutputs(ctx, deployment.ID, outputs); err != nil {
				return fmt.Errorf("persist post-deploy outputs: %w", err)
			}
		}
	}
	return nil
}

func (e HookExecutor) rollbackPostHook(ctx context.Context, app core.App, server core.Server, postErr error) error {
	cleaner, ok := e.Next.(CleanupExecutor)
	if !ok {
		return errors.Join(postErr, ErrCleanupUnsupported)
	}
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	if cleanupErr := cleaner.Cleanup(rollbackCtx, app, server, func(core.DeploymentState, string) error { return nil }); cleanupErr != nil {
		return errors.Join(postErr, fmt.Errorf("rollback deployment: %w", cleanupErr))
	}
	return fmt.Errorf("%w; deployment rolled back", postErr)
}

func (e HookExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	cleaner, ok := e.Next.(CleanupExecutor)
	if !ok {
		return ErrCleanupUnsupported
	}
	return cleaner.Cleanup(ctx, app, server, progress)
}

const maxGeneratedValuesBytes = 512 * 1024

const maxOutputFileBytes = 64 * 1024

var outputKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)

func hookEnvironment(deployment core.Deployment, app core.App, server core.Server, valuesPath, outputPath string) []string {
	environment := make([]string, 0, 16)
	for _, name := range []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR", "DOCKER_CONFIG"} {
		if value, ok := os.LookupEnv(name); ok {
			environment = append(environment, name+"="+value)
		}
	}
	// Operators can opt individual build or registry credentials into hooks
	// without exposing the controller's unrelated configuration and secrets.
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "DISPATCH_HOOK_") {
			environment = append(environment, entry)
		}
	}
	for name, value := range app.HookEnvironment {
		if (strings.HasPrefix(name, "DISPATCH_COMPONENT_") || strings.HasPrefix(name, "DISPATCH_EVENT_")) && len(value) <= 16*1024 {
			environment = append(environment, name+"="+value)
		}
	}
	return append(environment,
		"DISPATCH_APP_ID="+app.ID,
		"DISPATCH_APP_NAME="+app.Name,
		"DISPATCH_REVISION="+deployment.CommitSHA,
		"DISPATCH_SOURCE_REPOSITORY="+app.SourceRepo,
		"DISPATCH_SOURCE_BRANCH="+app.Branch,
		"DISPATCH_SERVER_NAME="+server.Name,
		"DISPATCH_DEPLOYMENT_URL="+app.Domain,
		"DISPATCH_VALUES_FILE="+valuesPath,
		"DISPATCH_OUTPUT_FILE="+outputPath,
	)
}

func readHookOutputs(path string) (map[string]string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > maxOutputFileBytes {
		return nil, errors.New("output file exceeds 64 KiB")
	}
	contents, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	var outputs map[string]string
	if err := json.Unmarshal(contents, &outputs); err != nil {
		return nil, errors.New("output file must be a JSON object of string values")
	}
	for key, value := range outputs {
		if !outputKey.MatchString(key) {
			return nil, fmt.Errorf("invalid output key %q", key)
		}
		if len(value) > 16*1024 {
			return nil, fmt.Errorf("output %q exceeds 16 KiB", key)
		}
		if key == "namespace" || key == "release" {
			return nil, fmt.Errorf("output %q is immutable", key)
		}
	}
	return outputs, nil
}

func (e HookExecutor) executeHook(ctx context.Context, script, workspace string, environment []string) error {
	run := e.runHook
	if run == nil {
		run = runShellHook
	}
	return run(ctx, script, workspace, environment)
}

func runShellHook(ctx context.Context, script, workspace string, environment []string) error {
	var output strings.Builder
	cmd := exec.CommandContext(ctx, "/bin/sh", "-euc", script)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = workspace, environment, &output, &output
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

func checkoutHookSource(ctx context.Context, deployment core.Deployment, app core.App, workspace string) error {
	args := []string{"clone", "--depth", "1"}
	if app.Branch != "" {
		args = append(args, "--branch", app.Branch)
	}
	args = append(args, app.SourceRepo, workspace)
	if err := runGit(ctx, args...); err != nil {
		return err
	}
	if deployment.CommitSHA == "" || deployment.CommitSHA == "HEAD" || deployment.CommitSHA == "chart" || deployment.CommitSHA == "inline" {
		return nil
	}
	if err := runGit(ctx, "-C", workspace, "fetch", "--depth", "1", "origin", deployment.CommitSHA); err != nil {
		return err
	}
	return runGit(ctx, "-C", workspace, "checkout", "--detach", deployment.CommitSHA)
}

func runGit(ctx context.Context, args ...string) error {
	var output strings.Builder
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdout, cmd.Stderr = &output, &output
	if token := os.Getenv("DISPATCH_GIT_TOKEN"); token != "" {
		cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Bearer "+token)
	}
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}

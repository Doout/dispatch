package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
)

type HelmExecutor struct {
	run commandFunc
}

var helmNamePart = regexp.MustCompile(`[^a-z0-9-]+`)

func (e HelmExecutor) Deploy(ctx context.Context, _ core.Deployment, app core.App, server core.Server, progress Progress) error {
	if err := ValidateHelmTarget(app, server); err != nil {
		return err
	}
	preparedServer, cleanupKubeconfig, err := prepareKubernetesServer(server)
	if err != nil {
		return err
	}
	defer cleanupKubeconfig()
	server = preparedServer
	workspace, err := os.MkdirTemp("", "dispatch-helm-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	configArgs := []string{"--repository-config", filepath.Join(workspace, "repositories.yaml"), "--repository-cache", filepath.Join(workspace, "repository-cache")}
	configArgs = append(configArgs, helmTargetArgs(server)...)
	chart := app.HelmChart
	if app.HelmRepository != "" {
		if err := progress(core.DeploymentFetching, "Loading Helm repository metadata"); err != nil {
			return err
		}
		alias := helmRepositoryAlias(app)
		args := append(append([]string{}, configArgs...), "repo", "add", alias, app.HelmRepository, "--force-update")
		username, password := os.Getenv("HELM_REPOSITORY_USERNAME"), os.Getenv("HELM_REPOSITORY_PASSWORD")
		if username != "" {
			args = append(args, "--username", username)
		}
		var passwordInput io.Reader
		if password != "" {
			args = append(args, "--password-stdin")
			passwordInput = strings.NewReader(password + "\n")
		}
		if err := e.commandWithInputAndOutput(ctx, passwordInput, "helm", args...); err != nil {
			return fmt.Errorf("add Helm repository: %w", err)
		}
		chart = alias + "/" + strings.TrimPrefix(chart, "/")
	} else if err := progress(core.DeploymentFetching, "Resolving Helm chart"); err != nil {
		return err
	}

	valuesPaths := []string{}
	if strings.TrimSpace(app.HelmValues) != "" {
		valuesPath := filepath.Join(workspace, "values.yaml")
		if err := os.WriteFile(valuesPath, []byte(app.HelmValues), 0o600); err != nil {
			return fmt.Errorf("write Helm values: %w", err)
		}
		valuesPaths = append(valuesPaths, valuesPath)
	}
	if strings.TrimSpace(app.HelmGeneratedValues) != "" {
		valuesPath := filepath.Join(workspace, "generated-values.yaml")
		if err := os.WriteFile(valuesPath, []byte(app.HelmGeneratedValues), 0o600); err != nil {
			return fmt.Errorf("write generated Helm values: %w", err)
		}
		valuesPaths = append(valuesPaths, valuesPath)
	}
	if strings.TrimSpace(app.HelmGroupValues) != "" {
		valuesPath := filepath.Join(workspace, "group-values.yaml")
		if err := os.WriteFile(valuesPath, []byte(app.HelmGroupValues), 0o600); err != nil {
			return fmt.Errorf("write preview group Helm values: %w", err)
		}
		valuesPaths = append(valuesPaths, valuesPath)
	}
	if err := progress(core.DeploymentBuilding, "Validating Helm release inputs"); err != nil {
		return err
	}
	release, namespace := helmReleaseName(app), helmNamespace(app, server)
	args := append(append([]string{}, configArgs...), "upgrade", "--install", release, chart,
		"--namespace", namespace, "--create-namespace", "--atomic", "--wait")
	if app.HelmVersion != "" {
		args = append(args, "--version", app.HelmVersion)
	}
	for _, valuesPath := range valuesPaths {
		args = append(args, "--values", valuesPath)
	}
	if err := progress(core.DeploymentStarting, "Installing Helm release "+release+" on "+server.Name); err != nil {
		return err
	}
	if err := e.commandWithOutput(ctx, "helm", args...); err != nil {
		return fmt.Errorf("install Helm release: %w", err)
	}
	if err := progress(core.DeploymentChecking, "Checking Helm release status"); err != nil {
		return err
	}
	statusArgs := append(append([]string{}, configArgs...), "status", release, "--namespace", namespace)
	if err := e.commandWithOutput(ctx, "helm", statusArgs...); err != nil {
		return fmt.Errorf("check Helm release: %w", err)
	}
	return progress(core.DeploymentRouting, routeMessage(app))
}

func (e HelmExecutor) Cleanup(ctx context.Context, app core.App, server core.Server, progress Progress) error {
	if err := ValidateHelmTarget(app, server); err != nil {
		return err
	}
	preparedServer, cleanupKubeconfig, err := prepareKubernetesServer(server)
	if err != nil {
		return err
	}
	defer cleanupKubeconfig()
	server = preparedServer
	release, namespace := helmReleaseName(app), helmNamespace(app, server)
	if err := progress(core.DeploymentStarting, "Removing Helm release "+release); err != nil {
		return err
	}
	args := append(helmTargetArgs(server), "uninstall", release, "--namespace", namespace, "--wait", "--ignore-not-found")
	if err := e.commandWithOutput(ctx, "helm", args...); err != nil {
		return fmt.Errorf("uninstall Helm release: %w", err)
	}
	return progress(core.DeploymentSucceeded, "Helm release removed")
}

func ValidateHelmTarget(app core.App, server core.Server) error {
	runtime := strings.ToLower(strings.TrimSpace(server.Runtime))
	if !core.IsKubernetesRuntime(runtime) {
		return errors.New("Helm applications require a Kubernetes server")
	}
	if server.Kubernetes == nil || (strings.TrimSpace(server.Kubernetes.KubeconfigPath) == "" && strings.TrimSpace(server.Kubernetes.KubeconfigData) == "") {
		return errors.New("Helm applications require a Kubernetes server with a kubeconfig")
	}
	chart := strings.TrimSpace(app.HelmChart)
	if chart == "" {
		return errors.New("Helm chart is required")
	}
	repository := strings.TrimSpace(app.HelmRepository)
	if repository != "" {
		if !strings.HasPrefix(repository, "https://") {
			return errors.New("Helm repository must use HTTPS")
		}
		if strings.Contains(chart, "://") {
			return errors.New("Helm chart must be relative when a repository is configured")
		}
		return nil
	}
	if !strings.HasPrefix(chart, "oci://") && !strings.HasPrefix(chart, "https://") {
		return errors.New("Helm chart must use OCI or HTTPS, or specify a Helm repository")
	}
	return nil
}

func prepareKubernetesServer(server core.Server) (core.Server, func(), error) {
	if server.Kubernetes == nil {
		return server, func() {}, nil
	}
	prepared, cleanup, err := kubeconfig.Prepare(*server.Kubernetes)
	if err != nil {
		return server, func() {}, fmt.Errorf("prepare kubeconfig: %w", err)
	}
	server.Kubernetes = &prepared
	return server, cleanup, nil
}

func helmReleaseName(app core.App) string {
	if value := normalizeHelmName(app.HelmRelease); value != "" {
		return value
	}
	suffix := strings.ToLower(app.ID)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	base := normalizeHelmName(app.Name)
	if base == "" {
		base = "app"
	}
	if suffix != "" {
		base += "-" + suffix
	}
	if len(base) > 53 {
		base = strings.Trim(base[:53], "-")
	}
	return base
}

func helmNamespace(app core.App, server core.Server) string {
	if value := normalizeHelmName(app.HelmNamespace); value != "" {
		return value
	}
	if server.Kubernetes != nil {
		if value := normalizeHelmName(server.Kubernetes.Namespace); value != "" {
			return value
		}
	}
	return "default"
}

func helmTargetArgs(server core.Server) []string {
	if server.Kubernetes == nil {
		return nil
	}
	args := []string{"--kubeconfig", server.Kubernetes.KubeconfigPath}
	if server.Kubernetes.Context != "" {
		args = append(args, "--kube-context", server.Kubernetes.Context)
	}
	return args
}

func helmRepositoryAlias(app core.App) string {
	value := normalizeHelmName("dispatch-" + app.ID)
	if len(value) > 53 {
		value = strings.Trim(value[:53], "-")
	}
	return value
}

func normalizeHelmName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = helmNamePart.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}

func (e HelmExecutor) commandWithOutput(ctx context.Context, name string, args ...string) error {
	return e.commandWithInputAndOutput(ctx, nil, name, args...)
}

func (e HelmExecutor) commandWithInputAndOutput(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	var output strings.Builder
	err := e.command(ctx, stdin, &output, name, args...)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output.String())
	if detail == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, detail)
}

func (e HelmExecutor) command(ctx context.Context, stdin io.Reader, output io.Writer, name string, args ...string) error {
	if e.run != nil {
		return e.run(ctx, stdin, output, name, args...)
	}
	return command(ctx, stdin, output, name, args...)
}

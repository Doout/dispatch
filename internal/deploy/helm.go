package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"encoding/json"
	"github.com/doout/dispatch/internal/chartvalues"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
)

const helmOperationTimeout = 5 * time.Minute

type HelmExecutor struct {
	newClient helmClientFactory
}

type helmClientFactory func(core.Server, string, string) (helmClient, error)

type helmClient interface {
	UpgradeInstall(context.Context, string, core.App, map[string]interface{}) error
	Status(context.Context, string) error
	Uninstall(context.Context, string) error
}

type sdkHelmClient struct {
	configuration *action.Configuration
	registry      *registry.Client
	settings      *cli.EnvSettings
}

var helmNamePart = regexp.MustCompile(`[^a-z0-9-]+`)

func (e HelmExecutor) Deploy(ctx context.Context, deployment core.Deployment, app core.App, server core.Server, progress Progress) error {
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

	if gitBackedHelmChart(app) {
		if err := progress(core.DeploymentFetching, "Checking out Helm chart source from "+app.SourceRepo); err != nil {
			return err
		}
		sourcePath := filepath.Join(workspace, "source")
		args := []string{"clone", "--depth", "1"}
		if app.Branch != "" && (deployment.CommitSHA == "" || deployment.CommitSHA == "HEAD" || deployment.CommitSHA == "chart") {
			args = append(args, "--branch", app.Branch)
		}
		args = append(args, app.SourceRepo, sourcePath)
		if err := runGitForApp(ctx, app, args...); err != nil {
			return fmt.Errorf("fetch Helm source: %w", err)
		}
		if deployment.CommitSHA != "" && deployment.CommitSHA != "HEAD" && deployment.CommitSHA != "chart" {
			if err := runGitForApp(ctx, app, "-C", sourcePath, "fetch", "--depth", "1", "origin", deployment.CommitSHA); err != nil {
				return fmt.Errorf("fetch Helm revision: %w", err)
			}
			if err := runGitForApp(ctx, app, "-C", sourcePath, "checkout", "--detach", deployment.CommitSHA); err != nil {
				return fmt.Errorf("checkout Helm revision: %w", err)
			}
		}
		chartPath, err := within(sourcePath, app.HelmChart)
		if err != nil {
			return fmt.Errorf("Helm chart path: %w", err)
		}
		app.HelmChart = chartPath
	} else if app.HelmRepository != "" {
		if err := progress(core.DeploymentFetching, "Loading Helm repository metadata"); err != nil {
			return err
		}
	} else if err := progress(core.DeploymentFetching, "Resolving Helm chart"); err != nil {
		return err
	}

	values, err := helmValues(app)
	if err != nil {
		return err
	}
	if err := progress(core.DeploymentBuilding, "Validating Helm release inputs"); err != nil {
		return err
	}

	release, namespace := helmReleaseName(app), helmNamespace(app, server)
	client, err := e.client(server, namespace, workspace)
	if err != nil {
		return fmt.Errorf("initialize Helm client: %w", err)
	}
	if err := progress(core.DeploymentStarting, "Installing Helm release "+release+" on "+server.Name); err != nil {
		return err
	}
	if err := client.UpgradeInstall(ctx, release, app, values); err != nil {
		return fmt.Errorf("install Helm release: %w", err)
	}
	if err := progress(core.DeploymentChecking, "Checking Helm release status"); err != nil {
		return err
	}
	if err := client.Status(ctx, release); err != nil {
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

	workspace, err := os.MkdirTemp("", "dispatch-helm-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)

	release, namespace := helmReleaseName(app), helmNamespace(app, server)
	client, err := e.client(server, namespace, workspace)
	if err != nil {
		return fmt.Errorf("initialize Helm client: %w", err)
	}
	if err := progress(core.DeploymentStarting, "Removing Helm release "+release); err != nil {
		return err
	}
	if err := client.Uninstall(ctx, release); err != nil {
		return fmt.Errorf("uninstall Helm release: %w", err)
	}
	return progress(core.DeploymentSucceeded, "Helm release removed")
}

func (e HelmExecutor) client(server core.Server, namespace, workspace string) (helmClient, error) {
	if e.newClient != nil {
		return e.newClient(server, namespace, workspace)
	}
	return newSDKHelmClient(server, namespace, workspace)
}

func newSDKHelmClient(server core.Server, namespace, workspace string) (helmClient, error) {
	settings := cli.New()
	settings.KubeConfig = server.Kubernetes.KubeconfigPath
	settings.KubeContext = server.Kubernetes.Context
	settings.RepositoryConfig = filepath.Join(workspace, "repositories.yaml")
	settings.RepositoryCache = filepath.Join(workspace, "repository-cache")
	settings.SetNamespace(namespace)

	registryClient, err := registry.NewClient(
		registry.ClientOptDebug(settings.Debug),
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize registry client: %w", err)
	}
	configuration := new(action.Configuration)
	if err := configuration.Init(settings.RESTClientGetter(), namespace, os.Getenv("HELM_DRIVER"), func(string, ...interface{}) {}); err != nil {
		return nil, err
	}
	configuration.RegistryClient = registryClient
	return &sdkHelmClient{configuration: configuration, registry: registryClient, settings: settings}, nil
}

// HelmReleaseManifest returns the rendered YAML stored with an installed release.
func HelmReleaseManifest(ctx context.Context, server core.Server, namespace, release string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return "", err
	}
	defer cleanup()
	workspace, err := os.MkdirTemp("", "dispatch-helm-inspect-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workspace)
	client, err := newSDKHelmClient(prepared, namespace, workspace)
	if err != nil {
		return "", err
	}
	sdk, ok := client.(*sdkHelmClient)
	if !ok {
		return "", errors.New("Helm release inspection is unavailable")
	}
	installed, err := action.NewGet(sdk.configuration).Run(release)
	if err != nil {
		return "", err
	}
	return installed.Manifest, nil
}

// HelmReleaseValues uses the stored Helm revision matching the selected deployment.
func HelmReleaseValues(ctx context.Context, server core.Server, namespace, release string, deployment core.Deployment, expected map[string]any) (chartvalues.Result, error) {
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return chartvalues.Result{}, err
	}
	defer cleanup()
	workspace, err := os.MkdirTemp("", "dispatch-values-inspect-")
	if err != nil {
		return chartvalues.Result{}, err
	}
	defer os.RemoveAll(workspace)
	client, err := newSDKHelmClient(prepared, namespace, workspace)
	if err != nil {
		return chartvalues.Result{}, err
	}
	sdk := client.(*sdkHelmClient)
	if err := ctx.Err(); err != nil {
		return chartvalues.Result{}, err
	}
	installed, err := action.NewGet(sdk.configuration).Run(release)
	if err != nil {
		return chartvalues.Result{}, err
	}
	if !releaseValuesMatch(installed, deployment, expected) {
		history, historyErr := action.NewHistory(sdk.configuration).Run(release)
		if historyErr != nil {
			return chartvalues.Result{}, historyErr
		}
		installed = matchingRelease(history, deployment, expected)
		if installed == nil {
			return chartvalues.Result{}, errors.New("no retained Helm revision matches this deployment")
		}
	}
	result, err := chartvalues.Analyze(installed.Chart, installed.Config)
	if err == nil {
		result.Values = redactSnapshotValues("", result.Values).(map[string]any)
		if merged, mergeErr := chartutil.CoalesceValues(installed.Chart, installed.Config); mergeErr == nil {
			values := redactSnapshotValues("", map[string]any(merged)).(map[string]any)
			chartvalues.PreviewTpl(installed.Chart, values, chartutil.ReleaseOptions{Name: installed.Name, Namespace: installed.Namespace, Revision: installed.Version, IsInstall: installed.Version == 1, IsUpgrade: installed.Version > 1}, &result)
		}
	}
	return result, err
}

func matchingRelease(history []*helmrelease.Release, deployment core.Deployment, expected map[string]any) *helmrelease.Release {
	var matched *helmrelease.Release
	for _, candidate := range history {
		if releaseValuesMatch(candidate, deployment, expected) && (matched == nil || candidate.Version > matched.Version) {
			matched = candidate
		}
	}
	return matched
}

func releaseValuesMatch(installed *helmrelease.Release, deployment core.Deployment, expected map[string]any) bool {
	if installed == nil || installed.Info == nil || installed.Info.LastDeployed.Time.Before(deployment.CreatedAt) || (deployment.FinishedAt != nil && installed.Info.LastDeployed.Time.After(*deployment.FinishedAt)) {
		return false
	}
	actualJSON, _ := json.Marshal(redactSnapshotValues("", installed.Config))
	expectedJSON, _ := json.Marshal(redactSnapshotValues("", expected))
	return bytes.Equal(actualJSON, expectedJSON)
}

func (c *sdkHelmClient) UpgradeInstall(ctx context.Context, release string, app core.App, values map[string]interface{}) error {
	chartOptions := action.ChartPathOptions{
		RepoURL: app.HelmRepository,
		Version: app.HelmVersion,
	}
	if app.HelmRepository != "" {
		chartOptions.Username = os.Getenv("HELM_REPOSITORY_USERNAME")
		chartOptions.Password = os.Getenv("HELM_REPOSITORY_PASSWORD")
	}
	locator := action.NewInstall(c.configuration)
	locator.ChartPathOptions = chartOptions
	locator.SetRegistryClient(c.registry)
	chartPath, err := locator.LocateChart(app.HelmChart, c.settings)
	if err != nil {
		return fmt.Errorf("resolve chart: %w", err)
	}
	chart, err := loader.Load(chartPath)
	if err != nil {
		return fmt.Errorf("load chart: %w", err)
	}
	if err := action.CheckDependencies(chart, chart.Metadata.Dependencies); err != nil {
		return fmt.Errorf("check chart dependencies: %w", err)
	}

	history := action.NewHistory(c.configuration)
	history.Max = 1
	_, err = history.Run(release)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		install := action.NewInstall(c.configuration)
		install.ChartPathOptions = chartOptions
		install.SetRegistryClient(c.registry)
		install.ReleaseName = release
		install.Namespace = c.settings.Namespace()
		install.CreateNamespace = true
		install.Atomic = true
		install.Wait = true
		install.Timeout = helmOperationTimeout
		_, err = install.RunWithContext(ctx, chart, values)
		return err
	}
	if err != nil {
		return err
	}

	upgrade := action.NewUpgrade(c.configuration)
	upgrade.ChartPathOptions = chartOptions
	upgrade.SetRegistryClient(c.registry)
	upgrade.Namespace = c.settings.Namespace()
	upgrade.ResetValues = true
	upgrade.Atomic = true
	upgrade.Wait = true
	upgrade.Timeout = helmOperationTimeout
	upgrade.MaxHistory = c.settings.MaxHistory
	_, err = upgrade.RunWithContext(ctx, release, chart, values)
	return err
}

func (c *sdkHelmClient) Status(ctx context.Context, release string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := action.NewStatus(c.configuration).Run(release)
	return err
}

func (c *sdkHelmClient) Uninstall(ctx context.Context, release string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	uninstall := action.NewUninstall(c.configuration)
	uninstall.IgnoreNotFound = true
	uninstall.Wait = true
	uninstall.Timeout = helmOperationTimeout
	_, err := uninstall.Run(release)
	return err
}

func helmValues(app core.App) (map[string]interface{}, error) {
	values := map[string]interface{}{}
	for _, layer := range []struct {
		name    string
		content string
	}{
		{name: "saved", content: app.HelmValues},
		{name: "generated", content: app.HelmGeneratedValues},
		{name: "preview group", content: app.HelmGroupValues},
	} {
		if strings.TrimSpace(layer.content) == "" {
			continue
		}
		current := map[string]interface{}{}
		decoder := yaml.NewDecoder(bytes.NewBufferString(layer.content))
		if err := decoder.Decode(&current); err != nil {
			return nil, fmt.Errorf("parse %s Helm values: %w", layer.name, err)
		}
		values = mergeHelmValues(values, current)
	}
	return values, nil
}

func mergeHelmValues(base, override map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{}, len(base))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range override {
		if next, ok := value.(map[string]interface{}); ok {
			if existing, ok := merged[key].(map[string]interface{}); ok {
				merged[key] = mergeHelmValues(existing, next)
				continue
			}
		}
		merged[key] = value
	}
	return merged
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
	if gitBackedHelmChart(app) {
		if !validGitSourceForExecution(app) {
			return errors.New("Git-backed Helm charts require an HTTPS repository, or an SSH repository with a configured key")
		}
		if _, err := within("/source", chart); err != nil {
			return fmt.Errorf("invalid Helm chart path: %w", err)
		}
		return nil
	}
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

func gitBackedHelmChart(app core.App) bool {
	chart := strings.TrimSpace(app.HelmChart)
	return strings.TrimSpace(app.SourceRepo) != "" && strings.TrimSpace(app.HelmRepository) == "" && !strings.Contains(chart, "://")
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

func normalizeHelmName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = helmNamePart.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}

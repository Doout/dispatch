package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
)

const maxInspectedHelmFileBytes = 2 * 1024 * 1024

type HelmChartMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	AppVersion  string `json:"appVersion,omitempty"`
	Type        string `json:"type,omitempty"`
}

type HelmValuesProfile struct {
	Path       string                 `json:"path"`
	Name       string                 `json:"name"`
	Values     map[string]interface{} `json:"values"`
	ValuesYAML string                 `json:"valuesYaml"`
}

type HelmChartInspection struct {
	Repository string                 `json:"repository"`
	Branch     string                 `json:"branch"`
	ChartPath  string                 `json:"chartPath"`
	Chart      HelmChartMetadata      `json:"chart"`
	Defaults   map[string]interface{} `json:"defaults"`
	ValuesYAML string                 `json:"valuesYaml"`
	Schema     interface{}            `json:"schema,omitempty"`
	Profiles   []HelmValuesProfile    `json:"profiles"`
}

// InspectHelmSource loads chart metadata and defaults from any supported Helm
// origin. Git sources retain profile discovery from files in the chart folder.
func InspectHelmSource(ctx context.Context, app core.App) (HelmChartInspection, error) {
	if gitBackedHelmChart(app) {
		return InspectGitHelmSource(ctx, app)
	}
	if strings.TrimSpace(app.HelmChart) == "" {
		return HelmChartInspection{}, errors.New("Helm chart is required")
	}
	workspace, err := os.MkdirTemp("", "dispatch-helm-inspect-")
	if err != nil {
		return HelmChartInspection{}, err
	}
	defer os.RemoveAll(workspace)
	settings := cli.New()
	settings.RepositoryConfig = filepath.Join(workspace, "repositories.yaml")
	settings.RepositoryCache = filepath.Join(workspace, "repository-cache")
	registryClient, err := registry.NewClient(
		registry.ClientOptEnableCache(true),
		registry.ClientOptWriter(io.Discard),
		registry.ClientOptCredentialsFile(settings.RegistryConfig),
	)
	if err != nil {
		return HelmChartInspection{}, fmt.Errorf("initialize registry client: %w", err)
	}
	options := action.ChartPathOptions{RepoURL: app.HelmRepository, Version: app.HelmVersion}
	if app.HelmRepository != "" {
		options.Username = os.Getenv("HELM_REPOSITORY_USERNAME")
		options.Password = os.Getenv("HELM_REPOSITORY_PASSWORD")
	}
	locator := action.NewInstall(new(action.Configuration))
	locator.ChartPathOptions = options
	locator.SetRegistryClient(registryClient)
	if err := ctx.Err(); err != nil {
		return HelmChartInspection{}, err
	}
	chartPath, err := locator.LocateChart(app.HelmChart, settings)
	if err != nil {
		return HelmChartInspection{}, fmt.Errorf("resolve chart: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return HelmChartInspection{}, err
	}
	loaded, err := loader.Load(chartPath)
	if err != nil {
		return HelmChartInspection{}, fmt.Errorf("load Helm chart: %w", err)
	}
	valuesYAML, err := yaml.Marshal(loaded.Values)
	if err != nil {
		return HelmChartInspection{}, fmt.Errorf("encode chart defaults: %w", err)
	}
	return inspectionFromChart(app.HelmRepository, "", app.HelmChart, loaded.Metadata.Name, loaded.Metadata.Description,
		loaded.Metadata.Version, loaded.Metadata.AppVersion, loaded.Metadata.Type, loaded.Values, string(valuesYAML), loaded.Schema, nil)
}

// NormalizeGitHelmSource accepts either a clone URL plus chart path or a
// GitHub-style /tree/<branch>/<path> URL. It returns deployment-ready parts.
func NormalizeGitHelmSource(repository, branch, chartPath string) (string, string, string) {
	repository, branch, chartPath = strings.TrimSpace(repository), strings.TrimSpace(branch), strings.TrimSpace(chartPath)
	parsed, err := url.Parse(repository)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return repository, branch, chartPath
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	tree := -1
	for index, part := range parts {
		if part == "tree" {
			tree = index
			break
		}
	}
	if tree < 2 || tree+1 >= len(parts) {
		return repository, branch, chartPath
	}
	parsed.Path = "/" + strings.Join(parts[:tree], "/")
	if !strings.HasSuffix(parsed.Path, ".git") {
		parsed.Path += ".git"
	}
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	if branch == "" || branch == "main" {
		branch = parts[tree+1]
	}
	if chartPath == "" && tree+2 < len(parts) {
		chartPath = strings.Join(parts[tree+2:], "/")
	}
	return parsed.String(), branch, chartPath
}

// RepositoryForSourceAuth converts an HTTPS clone URL to the equivalent SSH
// form when the caller selected a stored SSH key.
func RepositoryForSourceAuth(repository, authType string) string {
	if authType != SourceAuthSSHKey {
		return repository
	}
	parsed, err := url.Parse(strings.TrimSpace(repository))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return repository
	}
	return "git@" + parsed.Host + ":" + strings.TrimPrefix(parsed.Path, "/")
}

// InspectGitHelmSource performs a shallow, temporary checkout and returns the
// chart metadata and values needed by the source editor.
func InspectGitHelmSource(ctx context.Context, app core.App) (HelmChartInspection, error) {
	repository, branch, chartPath := NormalizeGitHelmSource(app.SourceRepo, app.Branch, app.HelmChart)
	app.SourceRepo, app.Branch, app.HelmChart = repository, branch, chartPath
	if app.Branch == "" {
		app.Branch = "main"
	}
	if strings.TrimSpace(app.SourceRepo) == "" || strings.TrimSpace(app.HelmChart) == "" {
		return HelmChartInspection{}, errors.New("repository and chart path are required")
	}
	if !gitBackedHelmChart(app) || !validGitSourceForExecution(app) {
		return HelmChartInspection{}, errors.New("enter an HTTPS repository, or an SSH repository with a configured key")
	}

	workspace, err := os.MkdirTemp("", "dispatch-helm-inspect-")
	if err != nil {
		return HelmChartInspection{}, err
	}
	defer os.RemoveAll(workspace)
	sourcePath := filepath.Join(workspace, "source")
	args := []string{"clone", "--depth", "1", "--filter=blob:none", "--sparse"}
	if app.Branch != "" {
		args = append(args, "--branch", app.Branch)
	}
	args = append(args, app.SourceRepo, sourcePath)
	if err := runGitForApp(ctx, app, args...); err != nil {
		return HelmChartInspection{}, fmt.Errorf("fetch Helm source: %w", err)
	}
	if err := runGitForApp(ctx, app, "-C", sourcePath, "sparse-checkout", "set", "--no-cone", "--", app.HelmChart); err != nil {
		return HelmChartInspection{}, fmt.Errorf("load chart directory: %w", err)
	}
	resolvedChartPath, err := within(sourcePath, app.HelmChart)
	if err != nil {
		return HelmChartInspection{}, fmt.Errorf("Helm chart path: %w", err)
	}
	if err := rejectHelmSourceSymlinks(resolvedChartPath); err != nil {
		return HelmChartInspection{}, err
	}
	loaded, err := loader.Load(resolvedChartPath)
	if err != nil {
		return HelmChartInspection{}, fmt.Errorf("load Helm chart: %w", err)
	}
	valuesYAML, err := readLimitedFile(filepath.Join(resolvedChartPath, "values.yaml"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return HelmChartInspection{}, fmt.Errorf("read chart defaults: %w", err)
	}

	profiles, err := inspectHelmProfiles(resolvedChartPath)
	if err != nil {
		return HelmChartInspection{}, err
	}
	metadata := loaded.Metadata
	return inspectionFromChart(app.SourceRepo, app.Branch, app.HelmChart, metadata.Name, metadata.Description,
		metadata.Version, metadata.AppVersion, metadata.Type, loaded.Values, valuesYAML, loaded.Schema, profiles)
}

func inspectionFromChart(repository, branch, chartPath, name, description, version, appVersion, chartType string, defaults map[string]interface{}, valuesYAML string, rawSchema []byte, profiles []HelmValuesProfile) (HelmChartInspection, error) {
	var schema interface{}
	if len(rawSchema) > 0 {
		if len(rawSchema) > maxInspectedHelmFileBytes {
			return HelmChartInspection{}, errors.New("values schema exceeds 2 MB")
		}
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			return HelmChartInspection{}, fmt.Errorf("parse values schema: %w", err)
		}
	}
	if defaults == nil {
		defaults = map[string]interface{}{}
	}
	if profiles == nil {
		profiles = []HelmValuesProfile{}
	}
	return HelmChartInspection{
		Repository: repository, Branch: branch, ChartPath: chartPath,
		Chart:    HelmChartMetadata{Name: name, Description: description, Version: version, AppVersion: appVersion, Type: chartType},
		Defaults: defaults, ValuesYAML: valuesYAML, Schema: schema, Profiles: profiles,
	}, nil
}

func rejectHelmSourceSymlinks(chartPath string) error {
	return filepath.WalkDir(chartPath, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("chart source contains unsupported symbolic link %q", filepath.Base(path))
		}
		return nil
	})
}

func inspectHelmProfiles(chartPath string) ([]HelmValuesProfile, error) {
	paths, err := filepath.Glob(filepath.Join(chartPath, "values-*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	profiles := make([]HelmValuesProfile, 0, len(paths))
	for _, path := range paths {
		content, err := readLimitedFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
		}
		values := map[string]interface{}{}
		if err := yaml.Unmarshal([]byte(content), &values); err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
		}
		base := filepath.Base(path)
		name := strings.TrimSuffix(strings.TrimPrefix(base, "values-"), filepath.Ext(base))
		profiles = append(profiles, HelmValuesProfile{Path: base, Name: name, Values: values, ValuesYAML: content})
	}
	return profiles, nil
}

func readLimitedFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > maxInspectedHelmFileBytes {
		return "", errors.New("file exceeds 2 MB")
	}
	contents, err := os.ReadFile(path)
	return string(contents), err
}

package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/oklog/ulid/v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/storage/driver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
)

var errPreviewNamespaceMissing = errors.New("The release namespace does not exist yet. Deployment will create it; admission validation can finish only after it exists.")

type ReleaseValidation struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message"`
}
type ReleaseResource struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}
type ReleasePreview struct {
	Review     core.DeploymentReview   `json:"review"`
	Ready      bool                    `json:"ready"`
	Revision   string                  `json:"revision"`
	SpecDigest string                  `json:"specDigest"`
	Checks     []ReleaseValidation     `json:"checks"`
	Resources  []ReleaseResource       `json:"resources"`
	Snapshot   core.DeploymentSnapshot `json:"-"`
}

// PreviewRelease performs reads and Kubernetes dry-run requests only. Hooks and
// image builds are intentionally excluded because they may have side effects.
func (s *Service) PreviewRelease(ctx context.Context, app core.App, server core.Server, revision string, sourceAuth SourceAuthExecutor) ReleasePreview {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	out := ReleasePreview{Review: core.DeploymentReview{ProjectID: app.ProjectID, AppSpecDigest: app.SpecDigest(), ServiceRevisions: map[string]int64{}}, Revision: revision, SpecDigest: app.SpecDigest(), Checks: []ReleaseValidation{}, Resources: []ReleaseResource{}}
	add := func(name, state, message string) {
		out.Checks = append(out.Checks, ReleaseValidation{name, state, message})
	}
	bindings, err := s.store.GetAppServiceBindings(ctx, app.ID)
	if err != nil {
		add("Service credentials", "failed", "Service bindings could not be read.")
		return out
	}
	out.Review.BindingsDigest = core.ServiceBindingConfigurationDigest(bindings)
	services := map[string]core.Service{}
	for _, binding := range bindings {
		service, err := s.store.GetService(ctx, binding.ServiceRef)
		if err != nil || service.ProjectID != app.ProjectID {
			add("Service credentials", "failed", "A service is missing or outside this project.")
			return out
		}
		services[service.ID] = service
		out.Review.ServiceRevisions[service.ID] = service.Revision
	}
	if serviceconn.ValidateBindings(bindings, app.BuildType, services) != nil {
		add("Service credentials", "failed", "A selected service field or destination is invalid.")
		return out
	}
	for _, binding := range bindings {
		service := services[binding.ServiceRef]
		values, err := s.services.Resolve(ctx, service)
		if err != nil {
			add("Service credentials", "failed", "A bound service credential cannot be resolved.")
			return out
		}
		app.ServiceRuntime = append(app.ServiceRuntime, core.ServiceRuntimeBinding{Binding: binding, Values: values})
		out.Snapshot.ServiceBindings = append(out.Snapshot.ServiceBindings, core.AppliedServiceBinding{Alias: binding.Alias, ServiceID: service.ID, ServiceName: service.Name, Revision: service.Revision})
	}
	add("Service credentials", "passed", "Selected fields and credentials resolve. Values are hidden.")
	app, err = sourceAuth.Resolve(ctx, app)
	if err != nil {
		add("Source credentials", "failed", "The configured repository credential cannot be resolved.")
		return out
	}
	add("Source credentials", "passed", "Repository authentication is configured.")
	out.Snapshot.TargetID, out.Snapshot.TargetName, out.Snapshot.Runtime = server.ID, server.Name, string(server.Runtime)
	if app.PreDeployHook != "" {
		add("Hooks", "unavailable", "The pre-deploy hook can change generated inputs. Hooks run only during deployment; this preview cannot validate their output.")
	}
	if app.BuildType != core.BuildTypeHelm {
		if app.SourceRepo != "" && !(app.BuildType == core.BuildTypeCompose && app.ComposeContent != "") {
			resolved, err := previewRuntimeSource(ctx, app, revision)
			if err != nil {
				add("Source", "failed", err.Error())
				return out
			}
			out.Revision = resolved
			add("Source", "passed", "The repository revision and runtime source paths are available. Deployment will use the resolved commit.")
		}
		if app.BuildType == core.BuildTypeCompose && app.ComposeContent != "" {
			dir, err := os.MkdirTemp("", "dispatch-preview-compose-")
			if err != nil {
				add("Compose", "failed", "Cannot create protected validation workspace.")
				return out
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "compose.yaml")
			if os.WriteFile(path, []byte(app.ComposeContent), 0600) != nil {
				add("Compose", "failed", "Cannot validate saved Compose definition.")
				return out
			}
			if _, err := composeServiceOverride(dir, path, app.ServiceRuntime); err != nil {
				add("Compose", "failed", "A selected Compose service or environment value is invalid.")
				return out
			}
			add("Compose", "passed", "Saved Compose service selections and environment mappings are valid.")
		}
		add("Runtime validation", "unavailable", "Image builds and container replacement are checked during deployment. Kubernetes dry-run applies to Helm releases.")
		out.Ready = true
		return out
	}
	if ValidateHelmTarget(app, server) != nil {
		add("Target", "failed", "A Kubernetes or OpenShift target is required.")
		return out
	}
	namespace, release := helmNamespace(app, server), helmReleaseName(app)
	out.Snapshot.Namespace, out.Snapshot.Release, out.Snapshot.Chart = namespace, release, app.HelmChart
	values, err := helmValues(app)
	if err != nil {
		add("Chart values", "failed", "Saved chart values are invalid YAML.")
		return out
	}
	out.Snapshot.Values = redactSnapshotValues("", values).(map[string]any)
	d := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, CommitSHA: revision}
	if helmServiceValues(d, app, values) != nil {
		add("Chart values", "failed", "A service destination conflicts with the chart values.")
		return out
	}
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		add("Target", "failed", "Target credentials are unavailable.")
		return out
	}
	defer cleanup()
	dir, err := os.MkdirTemp("", "dispatch-preview-helm-")
	if err != nil {
		add("Chart", "failed", "Cannot create protected validation workspace.")
		return out
	}
	defer os.RemoveAll(dir)
	if gitBackedHelmChart(app) {
		sourcePath := filepath.Join(dir, "source")
		args := []string{"clone", "--depth", "1"}
		if app.Branch != "" && (revision == "" || revision == "HEAD" || revision == "chart") {
			args = append(args, "--branch", app.Branch)
		}
		args = append(args, app.SourceRepo, sourcePath)
		if runGitForApp(ctx, app, args...) != nil {
			add("Source", "failed", "Cannot fetch the chart repository with the configured credential.")
			return out
		}
		if revision != "" && revision != "HEAD" && revision != "chart" {
			if runGitForApp(ctx, app, "-C", sourcePath, "fetch", "--depth", "1", "origin", revision) != nil || runGitForApp(ctx, app, "-C", sourcePath, "checkout", "--detach", revision) != nil {
				add("Source", "failed", "The selected source revision is unavailable.")
				return out
			}
		}
		var resolved strings.Builder
		if command(ctx, nil, &resolved, "git", "-C", sourcePath, "rev-parse", "HEAD") == nil {
			out.Revision = strings.TrimSpace(resolved.String())
		}
		app.HelmChart, err = within(sourcePath, app.HelmChart)
		if err != nil {
			add("Chart", "failed", "The chart path leaves the source directory.")
			return out
		}
	}
	genericClient, err := newSDKHelmClient(prepared, namespace, dir)
	if err != nil {
		add("Target", "failed", "Cannot connect to the deployment target.")
		return out
	}
	// Carry cancellation through discovery, Helm history reads, and dry-run calls.
	client := genericClient.(*sdkHelmClient)
	getter := driftRESTGetter{RESTClientGetter: client.settings.RESTClientGetter(), ctx: ctx}
	if err = client.configuration.Init(getter, namespace, os.Getenv("HELM_DRIVER"), func(string, ...any) {}); err != nil {
		add("Target", "failed", "Cannot initialize target validation.")
		return out
	}
	client.configuration.RegistryClient = client.registry
	options := action.ChartPathOptions{RepoURL: app.HelmRepository, Version: app.HelmVersion}
	if app.HelmRepository != "" {
		options.Username = os.Getenv("HELM_REPOSITORY_USERNAME")
		options.Password = os.Getenv("HELM_REPOSITORY_PASSWORD")
	}
	install := action.NewInstall(client.configuration)
	install.ChartPathOptions = options
	install.SetRegistryClient(client.registry)
	path, err := install.LocateChart(app.HelmChart, client.settings)
	if err != nil {
		add("Chart", "failed", "The selected chart or chart version could not be loaded.")
		return out
	}
	chart, err := loader.Load(path)
	if err != nil || action.CheckDependencies(chart, chart.Metadata.Dependencies) != nil {
		add("Chart", "failed", "Chart files or dependencies are invalid.")
		return out
	}
	install.ReleaseName, install.Namespace = release, namespace
	install.DryRun, install.DryRunOption = true, "server"
	install.DisableHooks = true
	install.Timeout = 30 * time.Second
	if _, e := action.NewHistory(client.configuration).Run(release); e == nil {
		install.IsUpgrade = true
	} else if !errors.Is(e, driver.ErrReleaseNotFound) {
		add("Release", "failed", "Cannot read retained release history.")
		return out
	}
	// ReplaceName avoids a name-in-use failure while rendering a dry-run upgrade.
	install.Replace = true
	rendered, err := install.RunWithContext(ctx, chart, values)
	if err != nil {
		add("Rendering", "failed", "Chart rendering or schema validation failed. Review chart values and target access.")
		return out
	}
	add("Rendering", "passed", "The chart renders with the selected values and service references.")
	manifest := rendered.Manifest
	for index, binding := range app.ServiceRuntime {
		data := map[string]string{}
		for key, field := range binding.Binding.Helm.Keys {
			data[key] = binding.Values[field]
		}
		raw, _ := json.Marshal(map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": serviceSecretName(d.ID, index), "namespace": namespace, "labels": map[string]string{"dispatch.app": app.ID, "dispatch.deployment": d.ID, "dispatch.service-binding": "true", "dispatch.release": release}}, "type": "Opaque", "immutable": true, "stringData": data})
		manifest += "\n---\n" + string(raw)
	}
	for _, hook := range rendered.Hooks {
		manifest += "\n---\n" + hook.Manifest
	}
	resources, err := dryRunReleaseManifest(ctx, client, namespace, manifest)
	out.Resources = resources
	if err != nil {
		if errors.Is(err, errPreviewNamespaceMissing) {
			add("Kubernetes admission", "unavailable", err.Error())
			out.Ready = true
			return out
		}
		add("Kubernetes admission", "failed", err.Error())
		return out
	}
	add("Kubernetes admission", "passed", "The API server accepted dry-run resource validation. No runtime resources were changed.")
	out.Ready = true
	return out
}

func previewRuntimeSource(ctx context.Context, app core.App, revision string) (string, error) {
	if !validGitSourceForExecution(app) {
		return "", errors.New("The configured source repository is not supported.")
	}
	dir, err := os.MkdirTemp("", "dispatch-preview-source-")
	if err != nil {
		return "", errors.New("Cannot prepare source validation.")
	}
	defer os.RemoveAll(dir)
	source := filepath.Join(dir, "source")
	args := []string{"clone", "--depth", "1"}
	if app.Branch != "" && (revision == "" || revision == "HEAD" || revision == "chart") {
		args = append(args, "--branch", app.Branch)
	}
	args = append(args, app.SourceRepo, source)
	if runGitForApp(ctx, app, args...) != nil {
		return "", errors.New("Cannot fetch the repository with the configured source credential.")
	}
	if revision != "" && revision != "HEAD" && revision != "chart" {
		if runGitForApp(ctx, app, "-C", source, "fetch", "--depth", "1", "origin", revision) != nil || runGitForApp(ctx, app, "-C", source, "checkout", "--detach", revision) != nil {
			return "", errors.New("The selected source revision is unavailable.")
		}
	}
	var resolved strings.Builder
	if command(ctx, nil, &resolved, "git", "-C", source, "rev-parse", "HEAD") != nil {
		return "", errors.New("Cannot identify the selected source commit.")
	}
	buildContext, err := within(source, app.ContextPath)
	if err != nil {
		return "", errors.New("The source context path is invalid.")
	}
	path := app.DockerfilePath
	if app.BuildType == core.BuildTypeCompose {
		path = app.ComposePath
	}
	path, err = within(buildContext, path)
	if err != nil {
		return "", errors.New("The runtime definition leaves the source directory.")
	}
	if file, err := os.Stat(path); err != nil || file.IsDir() {
		return "", errors.New("The Dockerfile or Compose definition is unavailable at the configured source path.")
	}
	if app.BuildType == core.BuildTypeCompose {
		if _, err := composeServiceOverride(dir, path, app.ServiceRuntime); err != nil {
			return "", errors.New("A selected Compose service or environment mapping is invalid for this revision.")
		}
	}
	return strings.TrimSpace(resolved.String()), nil
}

func dryRunReleaseManifest(ctx context.Context, client *sdkHelmClient, namespace, manifest string) ([]ReleaseResource, error) {
	getter := driftRESTGetter{RESTClientGetter: client.settings.RESTClientGetter(), ctx: ctx}
	config, err := getter.ToRESTConfig()
	if err != nil {
		return nil, errors.New("Target credentials are unavailable.")
	}
	dynamicClient, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, errors.New("Cannot initialize Kubernetes validation.")
	}
	mapper, err := getter.ToRESTMapper()
	if err != nil {
		return nil, errors.New("Kubernetes API discovery is unavailable.")
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	resources := []ReleaseResource{}
	for {
		var raw map[string]any
		if err = decoder.Decode(&raw); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return resources, errors.New("A rendered resource is invalid YAML.")
		}
		if len(raw) == 0 {
			continue
		}
		object := &unstructured.Unstructured{Object: raw}
		if object.GetName() == "" {
			return resources, errors.New("A rendered resource has no stable name and cannot be previewed.")
		}
		mapping, err := mapper.RESTMapping(object.GroupVersionKind().GroupKind(), object.GroupVersionKind().Version)
		if err != nil {
			return resources, errors.New("A rendered resource type is unavailable on the target. Install required CRDs before deployment.")
		}
		var resource dynamic.ResourceInterface
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			if object.GetNamespace() == "" {
				object.SetNamespace(namespace)
			}
			resource = dynamicClient.Resource(mapping.Resource).Namespace(object.GetNamespace())
		} else {
			resource = dynamicClient.Resource(mapping.Resource)
		}
		delete(object.Object, "status")
		object.SetManagedFields(nil)
		object.SetResourceVersion("")
		object.SetUID("")
		encoded, err := json.Marshal(object.Object)
		if err != nil {
			return resources, errors.New("A rendered resource cannot be validated.")
		}
		force := true // Dry-run only: validate intended Helm changes without claiming field ownership.
		if _, err = resource.Patch(ctx, object.GetName(), types.ApplyPatchType, encoded, metav1.PatchOptions{FieldManager: "dispatch-preview", Force: &force, DryRun: []string{metav1.DryRunAll}}); err != nil {
			if status, ok := err.(*apierrors.StatusError); ok && apierrors.IsNotFound(err) && status.ErrStatus.Details != nil && status.ErrStatus.Details.Kind == "namespaces" {
				return resources, errPreviewNamespaceMissing
			}
			return resources, errors.New("Kubernetes dry-run rejected " + object.GetKind() + "/" + object.GetName() + ". Check admission policy, permissions, field ownership, and whether the namespace exists. No changes were applied.")
		}
		resources = append(resources, ReleaseResource{Kind: object.GetKind(), Name: object.GetName(), Namespace: object.GetNamespace()})
		if len(resources) > 1000 {
			return resources, errors.New("Preview is limited to 1000 resources.")
		}
	}
	return resources, nil
}

package deploy

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

type recordingHelmClient struct {
	operation string
	release   string
	app       core.App
	values    map[string]interface{}
}

func (c *recordingHelmClient) UpgradeInstall(_ context.Context, release string, app core.App, values map[string]interface{}) error {
	c.operation, c.release, c.app, c.values = "upgrade-install", release, app, values
	return nil
}

func (c *recordingHelmClient) Status(_ context.Context, release string) error {
	c.operation += ",status"
	c.release = release
	return nil
}

func (c *recordingHelmClient) Uninstall(_ context.Context, release string) error {
	c.operation, c.release = "uninstall", release
	return nil
}

func TestHelmExecutorInstallsRepositoryChartWithMergedValuesAndTargetConfig(t *testing.T) {
	client := &recordingHelmClient{}
	var configuredServer core.Server
	configuredNamespace := ""
	executor := HelmExecutor{newClient: func(server core.Server, namespace, workspace string) (helmClient, error) {
		configuredServer, configuredNamespace = server, namespace
		if info, err := os.Stat(workspace); err != nil || !info.IsDir() {
			t.Fatalf("Helm workspace was not available: %v", err)
		}
		return client, nil
	}}
	app := core.App{ID: "app-12345678", Name: "Preview API", BuildType: core.BuildTypeHelm, HelmChart: "preview-api",
		HelmVersion: "1.2.3", HelmRepository: "https://charts.example.test", HelmValues: "image:\n  tag: pr-42\nreplicas: 1",
		HelmGeneratedValues: "image:\n  digest: sha256:1234",
		HelmGroupValues:     "image:\n  tag: group\nconfig:\n  backendUrl: https://service.example.test",
		HelmNamespace:       "preview-42", HelmRelease: "preview-api-42"}
	server := core.Server{Name: "cluster", Runtime: core.ServerRuntimeKubernetes,
		Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config/cluster", Context: "staging", Namespace: "fallback"}}
	states := []core.DeploymentState{}
	if err := executor.Deploy(context.Background(), core.Deployment{}, app, server, func(state core.DeploymentState, _ string) error {
		states = append(states, state)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if client.operation != "upgrade-install,status" || client.release != "preview-api-42" {
		t.Fatalf("unexpected Helm operations: operation=%q release=%q", client.operation, client.release)
	}
	if client.app.HelmChart != app.HelmChart || client.app.HelmRepository != app.HelmRepository || client.app.HelmVersion != app.HelmVersion {
		t.Fatalf("chart configuration was not preserved: %#v", client.app)
	}
	image, ok := client.values["image"].(map[string]interface{})
	if !ok || image["tag"] != "group" || image["digest"] != "sha256:1234" || client.values["replicas"] != 1 {
		t.Fatalf("values were not deeply merged in saved/generated/group order: %#v", client.values)
	}
	if configuredNamespace != "preview-42" || configuredServer.Kubernetes.KubeconfigPath != "/config/cluster" || configuredServer.Kubernetes.Context != "staging" {
		t.Fatalf("unexpected Kubernetes target: namespace=%q server=%#v", configuredNamespace, configuredServer.Kubernetes)
	}
	if !slices.Equal(states, []core.DeploymentState{core.DeploymentFetching, core.DeploymentBuilding, core.DeploymentStarting, core.DeploymentChecking, core.DeploymentRouting}) {
		t.Fatalf("unexpected progress states: %#v", states)
	}
}

func TestHelmExecutorCleansUpRelease(t *testing.T) {
	client := &recordingHelmClient{}
	configuredNamespace := ""
	executor := HelmExecutor{newClient: func(_ core.Server, namespace, _ string) (helmClient, error) {
		configuredNamespace = namespace
		return client, nil
	}}
	app := core.App{ID: "app-12345678", Name: "Preview", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/charts/preview"}
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config/cluster", Namespace: "previews"}}
	if err := executor.Cleanup(context.Background(), app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if client.operation != "uninstall" || client.release != "preview-app-1234" || configuredNamespace != "previews" {
		t.Fatalf("unexpected cleanup: operation=%q release=%q namespace=%q", client.operation, client.release, configuredNamespace)
	}
}

func TestHelmExecutorMaterializesStoredKubeconfigForOneOperation(t *testing.T) {
	stored := `apiVersion: v1
kind: Config
current-context: preview
contexts:
  - name: preview
    context: {cluster: cluster, user: user}
clusters:
  - name: cluster
    cluster: {server: https://cluster.example.test}
users:
  - name: user
    user: {token: private-token}
`
	materializedPath := ""
	client := &recordingHelmClient{}
	executor := HelmExecutor{newClient: func(server core.Server, _ string, _ string) (helmClient, error) {
		materializedPath = server.Kubernetes.KubeconfigPath
		contents, err := os.ReadFile(materializedPath)
		if err != nil || !strings.Contains(string(contents), "private-token") {
			t.Fatalf("stored kubeconfig was not available to the Helm SDK: %q err=%v", contents, err)
		}
		return client, nil
	}}
	app := core.App{ID: "stored-app", Name: "Stored", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/charts/stored"}
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigData: stored, Context: "preview"}}
	if err := executor.Deploy(context.Background(), core.Deployment{}, app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if materializedPath == "" {
		t.Fatal("Helm SDK did not receive a kubeconfig path")
	}
	if _, err := os.Stat(materializedPath); !os.IsNotExist(err) {
		t.Fatalf("materialized kubeconfig was not removed: %v", err)
	}
}

func TestHelmValuesRejectsInvalidLayer(t *testing.T) {
	_, err := helmValues(core.App{HelmValues: "valid: true", HelmGeneratedValues: "broken: ["})
	if err == nil || !strings.Contains(err.Error(), "generated Helm values") {
		t.Fatalf("expected generated values parse error, got %v", err)
	}
}

func TestValidateHelmTargetRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config/cluster"}}
	for name, app := range map[string]core.App{
		"missing chart":       {},
		"insecure repository": {HelmChart: "service", HelmRepository: "http://charts.example.test"},
		"unqualified chart":   {HelmChart: "service"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateHelmTarget(app, server); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

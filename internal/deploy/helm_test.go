package deploy

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestHelmExecutorInstallsRepositoryChartWithValuesAndTargetConfig(t *testing.T) {
	t.Setenv("HELM_REPOSITORY_USERNAME", "chart-reader")
	t.Setenv("HELM_REPOSITORY_PASSWORD", "secret")
	commands := [][]string{}
	values := []string{}
	passwordInput := ""
	executor := HelmExecutor{run: func(_ context.Context, stdin io.Reader, _ io.Writer, name string, args ...string) error {
		if name != "helm" {
			t.Fatalf("expected helm command, got %q", name)
		}
		commands = append(commands, append([]string{}, args...))
		if slices.Contains(args, "--password-stdin") {
			contents, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatal(err)
			}
			passwordInput = string(contents)
		}
		for index, arg := range args {
			if arg == "--values" {
				contents, err := os.ReadFile(args[index+1])
				if err != nil {
					t.Fatal(err)
				}
				values = append(values, string(contents))
			}
		}
		return nil
	}}
	app := core.App{ID: "app-12345678", Name: "Preview API", BuildType: core.BuildTypeHelm, HelmChart: "preview-api",
		HelmVersion: "1.2.3", HelmRepository: "https://charts.example.test", HelmValues: "image:\n  tag: pr-42",
		HelmGeneratedValues: "image:\n  digest: sha256:1234",
		HelmGroupValues:     "config:\n  backendUrl: https://service.example.test",
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
	if len(commands) != 3 {
		t.Fatalf("expected repository, upgrade, and status commands, got %#v", commands)
	}
	assertArgs(t, commands[0], "repo", "add", "dispatch-app-12345678", "--username", "chart-reader", "--password-stdin")
	if slices.Contains(commands[0], "secret") || passwordInput != "secret\n" {
		t.Fatalf("repository password must be supplied only through stdin: args=%#v stdin=%q", commands[0], passwordInput)
	}
	assertArgs(t, commands[1], "upgrade", "--install", "preview-api-42", "dispatch-app-12345678/preview-api", "--namespace", "preview-42", "--version", "1.2.3", "--kubeconfig", "/config/cluster", "--kube-context", "staging")
	assertArgs(t, commands[2], "status", "preview-api-42")
	if !slices.Equal(values, []string{app.HelmValues, app.HelmGeneratedValues, app.HelmGroupValues}) {
		t.Fatalf("expected saved, generated, and group values in order, got %#v", values)
	}
	if !slices.Equal(states, []core.DeploymentState{core.DeploymentFetching, core.DeploymentBuilding, core.DeploymentStarting, core.DeploymentChecking, core.DeploymentRouting}) {
		t.Fatalf("unexpected progress states: %#v", states)
	}
}

func TestHelmExecutorCleansUpRelease(t *testing.T) {
	var command []string
	executor := HelmExecutor{run: func(_ context.Context, _ io.Reader, _ io.Writer, _ string, args ...string) error {
		command = append([]string{}, args...)
		return nil
	}}
	app := core.App{ID: "app-12345678", Name: "Preview", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/charts/preview"}
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config/cluster", Namespace: "previews"}}
	if err := executor.Cleanup(context.Background(), app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	assertArgs(t, command, "uninstall", "preview-app-1234", "--namespace", "previews", "--ignore-not-found", "--kubeconfig", "/config/cluster")
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
	executor := HelmExecutor{run: func(_ context.Context, _ io.Reader, _ io.Writer, _ string, args ...string) error {
		for index, arg := range args {
			if arg != "--kubeconfig" {
				continue
			}
			materializedPath = args[index+1]
			contents, err := os.ReadFile(materializedPath)
			if err != nil || !strings.Contains(string(contents), "private-token") {
				t.Fatalf("stored kubeconfig was not available to Helm: %q err=%v", contents, err)
			}
		}
		return nil
	}}
	app := core.App{ID: "stored-app", Name: "Stored", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/charts/stored"}
	server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigData: stored, Context: "preview"}}
	if err := executor.Deploy(context.Background(), core.Deployment{}, app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if materializedPath == "" {
		t.Fatal("Helm command did not receive a kubeconfig path")
	}
	if _, err := os.Stat(materializedPath); !os.IsNotExist(err) {
		t.Fatalf("materialized kubeconfig was not removed: %v", err)
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

func assertArgs(t *testing.T, actual []string, expected ...string) {
	t.Helper()
	joined := "\x00" + strings.Join(actual, "\x00") + "\x00"
	for _, value := range expected {
		if !strings.Contains(joined, "\x00"+value+"\x00") {
			t.Fatalf("expected argument %q in %#v", value, actual)
		}
	}
}

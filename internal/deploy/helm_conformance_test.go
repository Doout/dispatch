package deploy

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/runtimecontract/conformance"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

// The Helm SDK uses its actual release store and renderer here. Kubernetes I/O
// uses Helm's fake client; live cluster lifecycle validation is a separate gate.
func TestHelmRuntimeConformance(t *testing.T) {
	t.Setenv("HELM_DRIVER", "memory")
	conformance.Run(t, func(t *testing.T) conformance.Fixture {
		var mutations bytes.Buffer
		settings := cli.New()
		settings.SetNamespace("isolated")
		configuration := &action.Configuration{Releases: storage.Init(driver.NewMemory()), KubeClient: &kubefake.PrintingKubeClient{Out: &mutations}, Capabilities: chartutil.DefaultCapabilities, Log: func(string, ...interface{}) {}}
		app := core.App{ID: "conformance-app", ProjectID: "project", BuildType: core.BuildTypeHelm, HelmRelease: "owned", HelmNamespace: "isolated", HelmChart: t.TempDir()}
		if err := os.WriteFile(filepath.Join(app.HelmChart, "Chart.yaml"), []byte("apiVersion: v2\nname: owned\nversion: 1.0.0\n"), 0600); err != nil {
			t.Fatal(err)
		}
		server := core.Server{Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/fixture", Namespace: "isolated"}}
		manifest := (HelmExecutor{}).RuntimeCapabilities(app, server)
		return conformance.Fixture{Manifest: manifest, MutationCount: func() int { return mutations.Len() }, ResourceCount: func() int {
			items, err := configuration.Releases.ListDeployed()
			if err != nil {
				t.Fatal(err)
			}
			return len(items)
		}, Execute: func(ctx context.Context, op runtimecontract.Operation) error {
			if err := manifest.Check(ctx, op); err != nil {
				return err
			}
			// Recreate the client on every call to exercise retained ownership.
			client := &sdkHelmClient{configuration: configuration, settings: settings, server: server}
			if op == runtimecontract.Destroy {
				return client.Uninstall(ctx, "owned", app)
			}
			return client.UpgradeInstall(ctx, "owned", app, core.Deployment{ID: "accepted", SpecDigest: "spec", CommitSHA: "source"}, nil)
		}}
	})
}

package deploy

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/runtimecontract"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/cli"
	kubefake "helm.sh/helm/v3/pkg/kube/fake"
	helmrelease "helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
)

func TestHelmSDKRejectsForeignReleaseBeforeUpgradeAndCleanup(t *testing.T) {
	t.Setenv("HELM_DRIVER", "memory")
	var mutations bytes.Buffer
	settings := cli.New()
	settings.SetNamespace("isolated")
	configuration := &action.Configuration{Releases: storage.Init(driver.NewMemory()), KubeClient: &kubefake.PrintingKubeClient{Out: &mutations}, Capabilities: chartutil.DefaultCapabilities, Log: func(string, ...interface{}) {}}
	client := &sdkHelmClient{configuration: configuration, settings: settings}
	app := core.App{ID: "app-1", ProjectID: "project-1", Name: "Owned", BuildType: core.BuildTypeHelm, HelmRelease: "owned", HelmNamespace: "isolated", HelmChart: t.TempDir()}
	if err := os.WriteFile(filepath.Join(app.HelmChart, "Chart.yaml"), []byte("apiVersion: v2\nname: owned\nversion: 1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	deployment := core.Deployment{ID: "new-operation", SpecDigest: "accepted-spec", CommitSHA: "accepted-source"}
	owner := app
	owner.ID = "other-app"
	metadata := newHelmDeploymentMetadata(owner, deployment)
	for version := 1; version <= 2; version++ {
		release := &helmrelease.Release{Name: "owned", Namespace: "isolated", Version: version, Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "owned", Version: "1.0.0"}}, Info: &helmrelease.Info{Status: helmrelease.StatusDeployed, Description: metadata.description()}}
		if err := configuration.Releases.Create(release); err != nil {
			t.Fatal(err)
		}
	}
	for _, operation := range []func() error{
		func() error { return client.UpgradeInstall(context.Background(), "owned", app, deployment, nil) },
		func() error { return client.Uninstall(context.Background(), "owned", app) },
	} {
		var ownership *runtimecontract.Error
		if err := operation(); !errors.As(err, &ownership) || ownership.Code != runtimecontract.OwnershipConflict {
			t.Fatalf("foreign release not rejected: %v", err)
		}
	}
	history, err := configuration.Releases.History("owned")
	if err != nil || len(history) != 2 || mutations.Len() != 0 {
		t.Fatal("foreign release changed", history, err, mutations.String())
	}
}

func TestHelmAtomicUpgradeChecksOlderSuccessfulRollbackCandidate(t *testing.T) {
	t.Setenv("HELM_DRIVER", "memory")
	var mutations bytes.Buffer
	settings := cli.New()
	settings.SetNamespace("isolated")
	configuration := &action.Configuration{Releases: storage.Init(driver.NewMemory()), KubeClient: &kubefake.PrintingKubeClient{Out: &mutations}, Capabilities: chartutil.DefaultCapabilities, Log: func(string, ...interface{}) {}}
	server := core.Server{Kubernetes: &core.KubernetesServerConfig{Namespace: "isolated", Validation: &core.KubernetesTargetEvidence{ClusterUID: "cluster"}}}
	client := &sdkHelmClient{configuration: configuration, settings: settings, server: server}
	app := core.App{ID: "owner", ProjectID: "project", HelmRelease: "owned", HelmChart: t.TempDir()}
	if err := os.WriteFile(filepath.Join(app.HelmChart, "Chart.yaml"), []byte("apiVersion: v2\nname: owned\nversion: 1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	metadata := newHelmDeploymentMetadata(app, core.Deployment{ID: "previous"})
	for _, release := range []*helmrelease.Release{
		{Name: "owned", Namespace: "isolated", Version: 1, Info: &helmrelease.Info{Status: helmrelease.StatusSuperseded, Description: metadata.description()}, Manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: escaped\n  namespace: foreign\n"},
		{Name: "owned", Namespace: "isolated", Version: 2, Info: &helmrelease.Info{Status: helmrelease.StatusFailed, Description: metadata.description()}, Manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: safe\n  namespace: isolated\n"},
	} {
		if err := configuration.Releases.Create(release); err != nil {
			t.Fatal(err)
		}
	}
	err := client.UpgradeInstall(context.Background(), "owned", app, core.Deployment{ID: "new"}, nil)
	if err == nil || !strings.Contains(err.Error(), "outside the target") || mutations.Len() != 0 {
		t.Fatal("unchecked atomic rollback candidate accepted", err, mutations.String())
	}
	history, err := configuration.Releases.History("owned")
	if err != nil || len(history) != 2 {
		t.Fatal("history changed before rollback review", err)
	}
}

func TestHelmReleaseOwnershipSurvivesRestartButRejectsReplacement(t *testing.T) {
	app := core.App{ID: "app-1", ProjectID: "project-1", HelmRelease: "owned"}
	metadata := newHelmDeploymentMetadata(app, core.Deployment{ID: "accepted-operation", SpecDigest: "accepted-spec", CommitSHA: "accepted-source"})
	release := &helmrelease.Release{Name: "owned", Namespace: "isolated", Info: &helmrelease.Info{Description: metadata.description()}}
	if err := requireHelmReleaseOwner(release, app, "isolated"); err != nil {
		t.Fatal("owned release could not be recovered", err)
	}
	for _, change := range []func(){
		func() { release.Namespace = "another" },
		func() { release.Name = "unrelated" },
		func() { metadata.ProjectID = "another-project"; release.Info.Description = metadata.description() },
		func() { release.Info.Description = "Installed by another Helm client" },
	} {
		release.Name, release.Namespace, release.Info.Description = "owned", "isolated", metadata.description()
		change()
		if err := requireHelmReleaseOwner(release, app, "isolated"); err == nil {
			t.Fatal("replaced release accepted")
		}
	}
	if metadata.SpecDigest != "accepted-spec" || metadata.ChartCommitSHA != "accepted-source" {
		t.Fatal("metadata did not retain accepted deployment evidence")
	}
}

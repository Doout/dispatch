package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/core"
)

func TestNormalizeGitHelmSourceSplitsEnterpriseTreeURL(t *testing.T) {
	repository, branch, chartPath := NormalizeGitHelmSource("https://github.example.com/platform/charts/tree/main/helm/service", "main", "")
	if repository != "https://github.example.com/platform/charts.git" || branch != "main" || chartPath != "helm/service" {
		t.Fatalf("unexpected normalized source: %q %q %q", repository, branch, chartPath)
	}
	if sshRepository := RepositoryForSourceAuth(repository, SourceAuthSSHKey); sshRepository != "git@github.example.com:platform/charts.git" {
		t.Fatalf("unexpected SSH repository: %q", sshRepository)
	}
}

func TestInspectGitHelmSourceLoadsDefaultsSchemaAndProfiles(t *testing.T) {
	repository := t.TempDir()
	chartPath := filepath.Join(repository, "helm", "service")
	if err := os.MkdirAll(chartPath, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Chart.yaml":         "apiVersion: v2\nname: service\ndescription: Test chart\ntype: application\nversion: 1.2.3\nappVersion: 4.5.6\n",
		"values.yaml":        "replicas: 1\nimage:\n  tag: latest\nenabled: true\n",
		"values.schema.json": `{"type":"object","properties":{"replicas":{"type":"integer","minimum":1}}}`,
		"values-slot.yaml":   "replicas: 3\n",
		"templates/x.yaml":   "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: test\n",
	}
	for name, content := range files {
		path := filepath.Join(chartPath, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "chart"}, {"branch", "-M", "main"}} {
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	inspection, err := InspectGitHelmSource(context.Background(), core.App{SourceRepo: "file://" + repository, Branch: "main", HelmChart: "helm/service", BuildType: core.BuildTypeHelm})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Chart.Name != "service" || inspection.Chart.Version != "1.2.3" || inspection.Defaults["replicas"] != float64(1) && inspection.Defaults["replicas"] != 1 {
		t.Fatalf("unexpected inspection: %#v", inspection)
	}
	if inspection.Schema == nil || len(inspection.Profiles) != 1 || inspection.Profiles[0].Name != "slot" {
		t.Fatalf("schema or profile missing: %#v", inspection)
	}
}

func TestInspectHelmSourceLoadsLocalPackagedOriginDefaults(t *testing.T) {
	chartPath := filepath.Join(t.TempDir(), "service")
	if err := os.MkdirAll(chartPath, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: service\nversion: 2.0.0\nappVersion: 8.1.0\n",
		"values.yaml": "replicaCount: 2\nimage:\n  repository: example/service\n  tag: stable\n",
	} {
		if err := os.WriteFile(filepath.Join(chartPath, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inspection, err := InspectHelmSource(context.Background(), core.App{HelmChart: chartPath, BuildType: core.BuildTypeHelm})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Chart.Name != "service" || inspection.Chart.Version != "2.0.0" || inspection.Defaults["replicaCount"] != float64(2) && inspection.Defaults["replicaCount"] != 2 {
		t.Fatalf("unexpected inspection: %#v", inspection)
	}
}

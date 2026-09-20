package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	"helm.sh/helm/v3/pkg/action"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This test writes only to its unique namespace in an explicitly supplied
// disposable cluster. Comparison itself must create no Helm or Kubernetes data.
func TestHelmAutomaticEquivalenceIntegration(t *testing.T) {
	kubeconfig := os.Getenv("DISPATCH_RELEASE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set DISPATCH_RELEASE_KUBECONFIG to a disposable cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "equivalence.db"))
	must(err)
	defer data.Close()
	must(data.Migrate(ctx))
	now := time.Now().UTC()
	name := "equivalence-" + strings.ToLower(ulid.Make().String())
	must(data.CreateProject(ctx, core.Project{ID: name, Name: name, CreatedAt: now}))
	server := core.Server{ID: name, Name: name, Runtime: "kubernetes", State: "ready", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: kubeconfig}, CreatedAt: now}
	must(data.CreateServer(ctx, server))
	kube, err := serviceKubeClient(server)
	must(err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = kube.CoreV1().Namespaces().Delete(cleanup, name, metav1.DeleteOptions{})
	})
	repo := t.TempDir()
	manifest := `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  color: {{ .Values.color | quote }}
{{- if .Values.note }}
  note: {{ .Values.note | quote }}
{{- end }}
`
	serviceFixtureRepo(t, repo, map[string]string{"chart/Chart.yaml": "apiVersion: v2\nname: equivalence\nversion: 0.1.0\n", "chart/values.yaml": "color: blue\nnote: retained\n", "chart/templates/config.yaml": manifest, "chart/README.md": "Documentation for now and randAlphaNum helpers\n"})
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture Git command failed: %v", err)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(path, contents string) string {
		t.Helper()
		must(os.WriteFile(filepath.Join(repo, path), []byte(contents), 0600))
		git("add", ".")
		git("-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "Update fixture")
		return git("rev-parse", "HEAD")
	}
	sha := git("rev-parse", "HEAD")
	app := core.App{ID: name, Name: "Equivalence", ProjectID: name, ServerID: name, BuildType: core.BuildTypeHelm, SourceRepo: "file://" + repo, Branch: "main", HelmChart: "chart", HelmRelease: "equivalence", HelmNamespace: name, HelmValues: "color: blue\nnote: retained", State: "ready", CreatedAt: now}
	must(data.CreateApp(ctx, app))
	s := NewService(data, SnapshotExecutor{Next: HelmExecutor{}, Store: data})
	s.ConfigureHelmComparison(SourceAuthExecutor{})
	wait := func(d core.Deployment) core.Deployment {
		t.Helper()
		for !d.State.Terminal() {
			select {
			case <-ctx.Done():
				t.Fatal("deployment timed out")
			case <-time.After(30 * time.Millisecond):
			}
			d, err = data.GetDeployment(ctx, d.ID)
			must(err)
		}
		if d.State != core.DeploymentSucceeded {
			t.Fatalf("fixture deployment failed: %s", d.Message)
		}
		return d
	}
	first, err := s.Start(ctx, app.ID, sha)
	must(err)
	first = wait(first)
	client, err := newSDKHelmClient(server, name, t.TempDir())
	must(err)
	version := func() int {
		t.Helper()
		rel, err := action.NewGet(client.(*sdkHelmClient).configuration).Run(app.HelmRelease)
		must(err)
		return rel.Version
	}
	assertUnchanged := func(revision string) {
		t.Helper()
		beforeVersion := version()
		before, err := data.ListDeployments(ctx, 50)
		must(err)
		d, unchanged, err := s.StartIfChanged(ctx, app.ID, revision)
		must(err)
		if !unchanged || d.ID == "" {
			t.Fatal("equivalent resources caused a deployment")
		}
		after, err := data.ListDeployments(ctx, 50)
		must(err)
		if len(after) != len(before) || version() != beforeVersion {
			t.Fatal("reuse changed Helm revision or deployment count")
		}
	}
	sha = commit("README.md", "An unrelated repository commit\n")
	assertUnchanged(sha)
	// An unrelated broken symlink cannot disqualify this chart's render.
	must(os.Symlink("/missing-unrelated-fixture", filepath.Join(repo, "unrelated-link")))
	sha = commit("README.md", "Another unrelated repository commit\n")
	assertUnchanged(sha)
	candidate := app
	candidate.HelmValues += "\nunused: changed"
	d, unchanged, err := s.ReuseCandidateIfUnchanged(ctx, candidate, sha, app.SpecDigest())
	must(err)
	if !unchanged || d.ID != first.ID {
		t.Fatal("unused candidate value was not reused")
	}
	saved, err := data.GetApp(ctx, app.ID)
	must(err)
	if saved.HelmValues != app.HelmValues {
		t.Fatal("candidate comparison changed saved inputs")
	}
	app = candidate
	must(data.UpdateApp(ctx, app))
	assertUnchanged(sha)
	app.HelmValues = "color: green\nnote: retained"
	must(data.UpdateApp(ctx, app))
	d, unchanged, err = s.StartIfChanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("used value change was skipped")
	}
	wait(d)
	config, err := kube.CoreV1().ConfigMaps(name).Get(ctx, app.HelmRelease, metav1.GetOptions{})
	must(err)
	if config.Data["color"] != "green" {
		t.Fatal("changed value was not installed")
	}
	app.HelmValues = "color: green\nnote: ''"
	must(data.UpdateApp(ctx, app))
	d, unchanged, err = s.StartIfChanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("removed rendered field was skipped")
	}
	wait(d)
	config, err = kube.CoreV1().ConfigMaps(name).Get(ctx, app.HelmRelease, metav1.GetOptions{})
	must(err)
	if _, present := config.Data["note"]; present {
		t.Fatal("rendered field was not removed")
	}
	beforeVersion := version()
	d, err = s.Start(ctx, app.ID, sha)
	must(err)
	wait(d)
	if version() != beforeVersion+1 {
		t.Fatal("explicit manual deployment was suppressed")
	}
	app.PreDeployHook = "true"
	must(data.UpdateApp(ctx, app))
	_, unchanged, err = s.ReuseIfUnchanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("application hook was suppressed")
	}
	app.PreDeployHook = ""
	must(data.UpdateApp(ctx, app))
	sha = commit("chart/templates/hook.yaml", "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: comparison-hook\n  annotations: {helm.sh/hook: pre-upgrade}\ndata: {value: test}\n")
	_, unchanged, err = s.ReuseIfUnchanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("chart hook was suppressed")
	}
	must(os.Remove(filepath.Join(repo, "chart/templates/hook.yaml")))
	sha = commit("chart/templates/config.yaml", manifest+"  timestamp: {{ now | quote }}\n")
	_, unchanged, err = s.ReuseIfUnchanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("time-dependent chart was suppressed")
	}
	sha = commit("chart/templates/config.yaml", manifest+"  random: {{ randAlphaNum 16 | quote }}\n")
	_, unchanged, err = s.ReuseIfUnchanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("random chart was suppressed")
	}
	sha = commit("chart/templates/config.yaml", manifest+"  revision: {{ .Release.Revision | quote }}\n")
	_, unchanged, err = s.ReuseIfUnchanged(ctx, app.ID, sha)
	must(err)
	if unchanged {
		t.Fatal("next Helm revision render was ignored")
	}
	beforeVersion = version()
	must(os.Symlink("/etc/hosts", filepath.Join(repo, "chart/templates/external.yaml")))
	sha = commit("chart/templates/config.yaml", manifest)
	_, unchanged, err = s.ReuseIfUnchanged(ctx, app.ID, sha)
	must(err)
	if unchanged || version() != beforeVersion {
		t.Fatal("external chart symlink was compared or runtime changed")
	}
	final, err := data.ListDeployments(ctx, 50)
	must(err)
	if len(final) != 4 {
		t.Fatalf("comparisons created deployment rows: got %d want 4", len(final))
	}
}

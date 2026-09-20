package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"helm.sh/helm/v3/pkg/chart"
	helmrelease "helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
)

const comparisonConfig = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: app\ndata:\n  color: blue\n  unused: retained\n"

func TestHelmManifestComparisonPreservesEveryRuntimeChange(t *testing.T) {
	base, err := canonicalHelmManifest(comparisonConfig)
	if err != nil {
		t.Fatal(err)
	}
	same, err := canonicalHelmManifest("# Different formatting\nkind: ConfigMap\nmetadata: {name: app}\napiVersion: v1\ndata: {unused: retained, color: blue}\n")
	if err != nil || !bytes.Equal(base, same) {
		t.Fatal("formatting changed equivalent resources")
	}
	for name, input := range map[string]string{
		"field removed":    strings.ReplaceAll(comparisonConfig, "  unused: retained\n", ""),
		"value changed":    strings.ReplaceAll(comparisonConfig, "blue", "green"),
		"resource added":   comparisonConfig + "---\napiVersion: v1\nkind: ConfigMap\nmetadata: {name: another}\ndata: {}\n",
		"annotation added": strings.ReplaceAll(comparisonConfig, "  name: app", "  name: app\n  annotations: {checksum: changed}"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := canonicalHelmManifest(input)
			if err != nil || bytes.Equal(base, got) {
				t.Fatal("actual runtime change was ignored")
			}
		})
	}
	first := "apiVersion: v1\nkind: Secret\nmetadata: {name: credential}\ndata: {password: b2xk}\n"
	second := strings.ReplaceAll(first, "b2xk", "bmV3")
	a, _ := canonicalHelmManifest(first)
	b, _ := canonicalHelmManifest(second)
	if bytes.Equal(a, b) {
		t.Fatal("credential changes were redacted before equality")
	}
	a, _ = canonicalHelmManifest(first + "---\n" + comparisonConfig)
	b, _ = canonicalHelmManifest(comparisonConfig + "---\n" + first)
	if !bytes.Equal(a, b) {
		t.Fatal("resource order affected comparison")
	}
	a, _ = canonicalHelmManifest(comparisonConfig + "items: [one, two]\n")
	b, _ = canonicalHelmManifest(comparisonConfig + "items: [two, one]\n")
	if bytes.Equal(a, b) {
		t.Fatal("list order was ignored")
	}
	for _, bad := range []string{"", comparisonConfig + "---\n" + comparisonConfig, "apiVersion: v1\nkind: ConfigMap\nmetadata: {generateName: random-}\n", strings.ReplaceAll(comparisonConfig, "  name: app", "  name: app\n  annotations: {helm.sh/hook: pre-upgrade}")} {
		if _, err := canonicalHelmManifest(bad); err == nil {
			t.Fatal("ambiguous manifest accepted")
		}
	}
}

func TestHelmComparisonRejectsTimeAndRandomHelpersIncludingDependencies(t *testing.T) {
	for _, helper := range []string{"now", "ago", "randAlphaNum", "uuidv4", "genPrivateKey", "encryptAES", "shuffle"} {
		c := &chart.Chart{Templates: []*chart.File{{Name: "templates/value.yaml", Data: []byte("value: {{ " + helper + " }}")}}}
		if stableHelmChart(c, nil) {
			t.Fatalf("variable helper %s accepted", helper)
		}
		parent := &chart.Chart{}
		parent.AddDependency(c)
		if stableHelmChart(parent, nil) {
			t.Fatal("dependency helper accepted")
		}
	}
	c := &chart.Chart{Templates: []*chart.File{{Name: "templates/config.yaml", Data: []byte(comparisonConfig)}}, Files: []*chart.File{{Name: "README.md", Data: []byte("Documentation mentions {{ now }}")}}}
	if !stableHelmChart(c, map[string]any{"unused": "now"}) {
		t.Fatal("non-templated documentation or literal values disabled comparison")
	}
	if stableHelmChart(c, map[string]any{"value": "{{ now }}"}) {
		t.Fatal("templated variable values accepted")
	}
	c.Values = map[string]any{"value": "{{ randAlphaNum 20 }}"}
	if stableHelmChart(c, nil) {
		t.Fatal("templated default values accepted")
	}
}

func TestHelmManifestComparisonPreservesLargeNumericValues(t *testing.T) {
	for _, manifest := range []string{
		"apiVersion: example.com/v1\nkind: Counter\nmetadata: {name: counter}\nspec: {value: 9007199254740992}\n",
		`{"apiVersion":"example.com/v1","kind":"Counter","metadata":{"name":"counter"},"spec":{"value":9007199254740992}}`,
	} {
		before, err := canonicalHelmManifest(manifest)
		if err != nil {
			t.Fatal(err)
		}
		after, err := canonicalHelmManifest(strings.ReplaceAll(manifest, "9007199254740992", "9007199254740993"))
		if err != nil || bytes.Equal(before, after) {
			t.Fatal("distinct integer inputs rounded into equivalent resources", err)
		}
	}
}

func TestHelmComparisonRequiresCurrentSuccessfulReleaseOwnership(t *testing.T) {
	now := time.Now().UTC()
	finish := now.Add(time.Minute)
	app := core.App{Name: "app", HelmRelease: "app", HelmNamespace: "ns"}
	server := core.Server{}
	d := core.Deployment{CreatedAt: now, FinishedAt: &finish, Snapshot: core.DeploymentSnapshot{Values: map[string]any{"color": "blue"}}}
	r := &helmrelease.Release{Name: "app", Namespace: "ns", Version: 2, Manifest: comparisonConfig, Config: map[string]any{"color": "blue"}, Info: &helmrelease.Info{Status: helmrelease.StatusDeployed, LastDeployed: helmtime.Time{Time: now.Add(time.Second)}}}
	if currentComparedRelease([]*helmrelease.Release{r}, app, server, d) != r {
		t.Fatal("matching installed release rejected")
	}
	for _, status := range []helmrelease.Status{helmrelease.StatusFailed, helmrelease.StatusPendingUpgrade, helmrelease.StatusSuperseded} {
		other := *r
		other.Version++
		other.Info = &helmrelease.Info{Status: status, LastDeployed: r.Info.LastDeployed}
		if currentComparedRelease([]*helmrelease.Release{r, &other}, app, server, d) != nil {
			t.Fatal("newer failed/pending release accepted as baseline")
		}
	}
	wrong := *r
	wrong.Config = map[string]any{"color": "green"}
	if currentComparedRelease([]*helmrelease.Release{&wrong}, app, server, d) != nil {
		t.Fatal("release does not match saved inputs")
	}
	wrong = *r
	wrong.Namespace = "other"
	if currentComparedRelease([]*helmrelease.Release{&wrong}, app, server, d) != nil {
		t.Fatal("cross-namespace baseline accepted")
	}
}

type comparisonExecutor struct{ runs atomic.Int32 }

func (e *comparisonExecutor) Deploy(context.Context, core.Deployment, core.App, core.Server, Progress) error {
	e.runs.Add(1)
	return nil
}

func comparisonFixture(t *testing.T) (*store.SQLStore, *Service, core.App, core.Deployment, *comparisonExecutor) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "comparison.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	project := core.Project{ID: "p", Name: "Project", CreatedAt: now}
	server := core.Server{ID: "target", Name: "Target", Runtime: "kubernetes", State: "ready", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/unused-test-config"}, CreatedAt: now}
	app := core.App{ID: "app", Name: "App", ProjectID: project.ID, ServerID: server.ID, BuildType: core.BuildTypeHelm, SourceRepo: "https://example.test/chart.git", HelmChart: "chart", HelmRelease: "app", HelmNamespace: "ns", HelmValues: "color: blue", CreatedAt: now}
	for _, err := range []error{data.CreateProject(ctx, project), data.CreateServer(ctx, server), data.CreateApp(ctx, app)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	finished := now.Add(20 * time.Second)
	d := core.Deployment{ID: "running", AppID: app.ID, CommitSHA: strings.Repeat("a", 40), State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CreatedAt: now.Add(time.Second), FinishedAt: &finished, Snapshot: core.DeploymentSnapshot{TargetID: server.ID, Runtime: server.Runtime, Namespace: "ns", Release: "app", Values: map[string]any{"color": "blue"}}}
	if err := data.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	e := &comparisonExecutor{}
	s := NewService(data, e)
	s.ConfigureHelmComparison(SourceAuthExecutor{})
	s.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) { return true, nil }
	return data, s, app, d, e
}

func waitComparisonDeployment(t *testing.T, data *store.SQLStore, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err := data.GetDeployment(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if d.State.Terminal() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("deployment did not complete")
}

func TestAutomaticHelmReuseDoesNotCreateOrRewriteDeployment(t *testing.T) {
	data, service, app, old, executor := comparisonFixture(t)
	ctx := context.Background()
	before, _ := data.GetDeployment(ctx, old.ID)
	d, unchanged, err := service.StartIfChanged(ctx, app.ID, strings.Repeat("b", 40))
	if err != nil || !unchanged || d.ID != old.ID {
		t.Fatalf("expected reuse: unchanged=%v err=%v", unchanged, err)
	}
	rows, _ := data.ListDeployments(ctx, 10)
	if len(rows) != 1 || executor.runs.Load() != 0 {
		t.Fatal("reuse created runtime work")
	}
	after, _ := data.GetDeployment(ctx, old.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("reuse rewrote historical deployment")
	}
	logs, _ := data.ListDeploymentLogs(ctx, old.ID, 0)
	if len(logs) != 0 {
		t.Fatal("reuse wrote historical logs")
	}
	d, err = service.Start(ctx, app.ID, strings.Repeat("b", 40))
	if err != nil || d.ID == old.ID {
		t.Fatal("explicit deployment was suppressed")
	}
	waitComparisonDeployment(t, data, d.ID)
	if executor.runs.Load() != 1 {
		t.Fatal("manual deployment did not execute")
	}
}

func TestAutomaticHelmReuseRejectsProofAfterConcurrentConfigurationEdit(t *testing.T) {
	data, service, app, old, _ := comparisonFixture(t)
	ctx := context.Background()
	service.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) {
		app.HelmValues = "color: green"
		return true, data.UpdateApp(ctx, app)
	}
	d, unchanged, err := service.StartIfChanged(ctx, app.ID, strings.Repeat("b", 40))
	if err != nil || unchanged || d.ID == old.ID {
		t.Fatalf("concurrent edit wrongly reused old release: unchanged=%v err=%v", unchanged, err)
	}
	waitComparisonDeployment(t, data, d.ID)
}

type editedComparisonStore struct {
	*store.SQLStore
	app    core.App
	edited bool
}

func (s *editedComparisonStore) GetApp(ctx context.Context, id string) (core.App, error) {
	app, err := s.SQLStore.GetApp(ctx, id)
	if err == nil && id == s.app.ID && !s.edited {
		s.edited = true
		err = s.SQLStore.UpdateApp(ctx, s.app)
	}
	return app, err
}

func TestSavedHelmComparisonRejectsEditBetweenApplicationReads(t *testing.T) {
	for _, start := range []bool{false, true} {
		t.Run(fmt.Sprint("start=", start), func(t *testing.T) {
			data, service, app, old, _ := comparisonFixture(t)
			ctx := context.Background()
			app.HelmValues = "color: green"
			service.store = &editedComparisonStore{SQLStore: data, app: app}
			service.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) {
				t.Fatal("stale saved inputs reached the renderer")
				return true, nil
			}
			var d core.Deployment
			var unchanged bool
			var err error
			if start {
				d, unchanged, err = service.StartIfChanged(ctx, app.ID, strings.Repeat("b", 40))
			} else {
				d, unchanged, err = service.ReuseIfUnchanged(ctx, app.ID, strings.Repeat("b", 40))
			}
			if err != nil || unchanged {
				t.Fatalf("concurrent edit was suppressed: unchanged=%v err=%v", unchanged, err)
			}
			if start {
				if d.ID == "" || d.ID == old.ID || d.SpecDigest != app.SpecDigest() {
					t.Fatal("ordinary acceptance did not capture updated application")
				}
				waitComparisonDeployment(t, data, d.ID)
			} else if d.ID != "" {
				t.Fatal("comparison returned the old release for stale inputs")
			}
			if _, err := data.GetHelmEquivalence(ctx, app.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("stale input comparison saved equivalence proof", err)
			}
		})
	}
}

func TestHelmCandidateRejectsEditSincePreparation(t *testing.T) {
	data, service, app, _, _ := comparisonFixture(t)
	ctx := context.Background()
	candidate, expected := app, app.SpecDigest()
	candidate.HelmValues += "\nunused: changed"
	app.HelmGroupValues = "color: green"
	if err := data.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	service.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) {
		t.Fatal("candidate captured before an app edit reached renderer")
		return true, nil
	}
	for _, expectedDigest := range []string{expected, ""} {
		if d, unchanged, err := service.ReuseCandidateIfUnchanged(ctx, candidate, strings.Repeat("b", 40), expectedDigest); err != nil || unchanged || d.ID != "" {
			t.Fatalf("stale or unanchored candidate was reused: unchanged=%v err=%v", unchanged, err)
		}
	}
	rows, _ := data.ListDeployments(ctx, 10)
	if len(rows) != 1 {
		t.Fatal("comparison changed deployment history")
	}
}

func TestHelmCandidateReusePreservesSavedApplication(t *testing.T) {
	data, service, app, old, _ := comparisonFixture(t)
	ctx := context.Background()
	candidate := app
	candidate.HelmValues += "\nunused: changed"
	d, unchanged, err := service.ReuseCandidateIfUnchanged(ctx, candidate, strings.Repeat("b", 40), app.SpecDigest())
	if err != nil || !unchanged || d.ID != old.ID {
		t.Fatalf("equivalent candidate not reused: %v", err)
	}
	saved, _ := data.GetApp(ctx, app.ID)
	if saved.HelmValues != app.HelmValues {
		t.Fatal("candidate overwrote saved application")
	}
	candidate.ServerID = "other"
	if _, unchanged, err = service.ReuseCandidateIfUnchanged(ctx, candidate, strings.Repeat("b", 40), app.SpecDigest()); err != nil || unchanged {
		t.Fatal("candidate crossed target identity")
	}
}

func TestHelmComparisonFailureNeverSuppressesAutomaticDeployment(t *testing.T) {
	data, service, app, old, _ := comparisonFixture(t)
	service.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) {
		return false, errors.New("private chart value must never escape")
	}
	d, unchanged, err := service.StartIfChanged(context.Background(), app.ID, strings.Repeat("b", 40))
	if err != nil || unchanged || d.ID == old.ID {
		t.Fatal("failed comparison suppressed ordinary deployment")
	}
	waitComparisonDeployment(t, data, d.ID)
	logs, _ := data.ListDeploymentLogs(context.Background(), d.ID, 0)
	for _, line := range logs {
		if strings.Contains(line.Message, "private chart") {
			t.Fatal("comparison leaked private chart error")
		}
	}
}

func TestAutomaticHelmDecisionHoldsApplicationLock(t *testing.T) {
	data, service, app, _, _ := comparisonFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	service.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) {
		close(entered)
		<-release
		return true, nil
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := service.ReuseIfUnchanged(context.Background(), app.ID, strings.Repeat("b", 40))
		done <- err
	}()
	<-entered
	started := make(chan core.Deployment, 1)
	go func() { d, _ := service.Start(context.Background(), app.ID, strings.Repeat("c", 40)); started <- d }()
	select {
	case <-started:
		t.Fatal("deployment bypassed comparison lock")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	d := <-started
	waitComparisonDeployment(t, data, d.ID)
}

func TestHelmReuseConservativeBoundaries(t *testing.T) {
	data, service, app, _, _ := comparisonFixture(t)
	ctx := context.Background()
	service.compareHelm = func(context.Context, core.App, core.Server, string, core.Deployment) (bool, error) {
		t.Fatal("ineligible input reached renderer")
		return false, nil
	}
	for _, revision := range []string{"", "HEAD", "main", "chart", "shortsha"} {
		if _, unchanged, err := service.ReuseIfUnchanged(ctx, app.ID, revision); err != nil || unchanged {
			t.Fatal("unpinned source was reused")
		}
	}
	for _, update := range []func(*core.App){func(a *core.App) { a.PreDeployHook = "true" }, func(a *core.App) { a.PostDeployHook = "true" }, func(a *core.App) { a.HookEnvironment = map[string]string{"value": "configured"} }} {
		candidate := app
		update(&candidate)
		if _, unchanged, err := service.ReuseCandidateIfUnchanged(ctx, candidate, strings.Repeat("b", 40), app.SpecDigest()); err != nil || unchanged {
			t.Fatal("hook configuration was reused")
		}
	}
	dependency := core.Service{ID: "dependency", ProjectID: app.ProjectID, Name: "dependency", Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"url": {Value: "private-runtime-value", Configured: true}}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := data.CreateService(ctx, dependency); err != nil {
		t.Fatal(err)
	}
	binding := core.ServiceBinding{Alias: "db", ServiceRef: dependency.ID, Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "url"}, SecretNameValues: []string{"database.existingSecret"}}}
	if err := data.ReplaceAppServiceBindings(ctx, app.ID, []core.ServiceBinding{binding}); err != nil {
		t.Fatal(err)
	}
	if _, unchanged, err := service.ReuseIfUnchanged(ctx, app.ID, strings.Repeat("b", 40)); err != nil || unchanged {
		t.Fatal("service credentials were reused without comparison evidence")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := service.StartIfChanged(cancelled, app.ID, strings.Repeat("b", 40)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled comparison accepted work")
	}
}

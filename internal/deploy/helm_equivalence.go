package deploy

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	helmrelease "helm.sh/helm/v3/pkg/release"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
)

type helmEquivalenceStore interface {
	SaveHelmEquivalence(context.Context, core.HelmEquivalence) error
}

// ConfigureHelmComparison enables read-only comparison for automatic Helm runs.
// Configure this before accepting work, using the runtime's source credentials.
func (s *Service) ConfigureHelmComparison(auth SourceAuthExecutor) {
	s.helmComparison = &auth
}

// StartIfChanged reuses an installed release only when its private rendered
// resources prove equivalent. Manual Start and StartReviewed always deploy.
func (s *Service) StartIfChanged(ctx context.Context, appID, commitSHA string) (core.Deployment, bool, error) {
	unlock := s.lockApp(appID)
	defer unlock()
	app, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, false, err
	}
	previous, unchanged, err := s.reuseHelmLocked(ctx, app, commitSHA, app.SpecDigest())
	if err != nil || unchanged {
		return previous, unchanged, err
	}
	d, err := s.startLocked(ctx, appID, commitSHA, nil)
	return d, false, err
}

// ReuseIfUnchanged checks a saved application without accepting any deployment.
func (s *Service) ReuseIfUnchanged(ctx context.Context, appID, commitSHA string) (core.Deployment, bool, error) {
	unlock := s.lockApp(appID)
	defer unlock()
	app, err := s.store.GetApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, false, err
	}
	return s.reuseHelmLocked(ctx, app, commitSHA, app.SpecDigest())
}

// ReuseCandidateIfUnchanged compares fully prepared workflow inputs without
// changing the saved application or the real deployment's logs and snapshots.
// expectedStoredSpecDigest must be captured before preparing the candidate.
func (s *Service) ReuseCandidateIfUnchanged(ctx context.Context, candidate core.App, commitSHA, expectedStoredSpecDigest string) (core.Deployment, bool, error) {
	unlock := s.lockApp(candidate.ID)
	defer unlock()
	return s.reuseHelmLocked(ctx, candidate, commitSHA, expectedStoredSpecDigest)
}

func (s *Service) reuseHelmLocked(ctx context.Context, candidate core.App, revision, expectedStoredSpecDigest string) (core.Deployment, bool, error) {
	var none core.Deployment
	if err := ctx.Err(); err != nil {
		return none, false, err
	}
	active, err := s.store.ActiveDeploymentForApp(ctx, candidate.ID)
	if err != nil {
		return none, false, err
	}
	if active != nil {
		return none, false, ErrDeploymentActive
	}
	if candidate.Template {
		return none, false, ErrApplicationTemplate
	}
	proofStore, supported := s.store.(helmEquivalenceStore)
	if !supported || s.helmComparison == nil || candidate.BuildType != core.BuildTypeHelm || !exactChartCommit(revision) || candidate.PreDeployHook != "" || candidate.PostDeployHook != "" || len(candidate.HookEnvironment) != 0 {
		return none, false, nil
	}
	original, err := s.store.GetApp(ctx, candidate.ID)
	if errors.Is(err, store.ErrNotFound) {
		return none, false, nil
	}
	if err != nil {
		return none, false, err
	}
	if expectedStoredSpecDigest == "" || original.SpecDigest() != expectedStoredSpecDigest || original.ProjectID != candidate.ProjectID || original.Name != candidate.Name || original.ServerID != candidate.ServerID || original.Template || original.BuildType != core.BuildTypeHelm || original.PreDeployHook != "" || original.PostDeployHook != "" || len(original.HookEnvironment) != 0 {
		return none, false, nil
	}
	bindings, err := s.store.GetAppServiceBindings(ctx, candidate.ID)
	if err != nil || len(bindings) != 0 || len(candidate.ServiceRuntime) != 0 {
		return none, false, nil
	}
	previous, err := s.store.LatestSuccessfulDeployment(ctx, candidate.ID)
	if err != nil || previous.FinishedAt == nil || len(previous.Snapshot.ServiceBindings) != 0 {
		return none, false, nil
	}
	server, err := s.store.GetServer(ctx, candidate.ServerID)
	if err != nil {
		return none, false, nil
	}
	if previous.Snapshot.TargetID != server.ID || previous.Snapshot.Namespace != helmNamespace(candidate, server) || previous.Snapshot.Release != helmReleaseName(candidate) || previous.Snapshot.Runtime != server.Runtime {
		return none, false, nil
	}
	if ValidateHelmTarget(candidate, server) != nil {
		return none, false, nil
	}
	comparisonCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	resolved, err := s.helmComparison.Resolve(comparisonCtx, candidate)
	if err != nil {
		return none, false, nil
	}
	compare := s.compareHelm
	if compare == nil {
		compare = renderHelmEquivalent
	}
	equivalent, err := compare(comparisonCtx, resolved, server, revision, previous)
	// Failed reads/renders provide no equivalence proof. Preserve ordinary
	// deployment behavior without exposing Helm output or credential details.
	if err != nil || !equivalent {
		if ctx.Err() != nil {
			return none, false, ctx.Err()
		}
		return none, false, nil
	}
	proof := core.HelmEquivalence{AppID: original.ID, ProjectID: original.ProjectID, AppName: original.Name, AppSpecDigest: original.SpecDigest(), CandidateSpecDigest: candidate.SpecDigest(), ChartCommit: revision, ServerID: server.ID, TargetDigest: core.HelmTargetDigest(server), DeploymentID: previous.ID, CheckedAt: time.Now().UTC()}
	// This transaction rechecks app/server/bindings and latest deployment under
	// database locks, including edits made while Git and Kubernetes were read.
	if err := proofStore.SaveHelmEquivalence(ctx, proof); err != nil {
		return none, false, nil
	}
	return previous, true, nil
}

func exactChartCommit(revision string) bool {
	if len(revision) != 40 && len(revision) != 64 {
		return false
	}
	_, err := hex.DecodeString(revision)
	return err == nil
}

// Only manifest formatting and resource document order are ignored. Unlike
// runtime drift comparison, this detects every removed field and resource.
func canonicalHelmManifest(manifest string) ([]byte, error) {
	if len(manifest) > 16<<20 {
		return nil, errors.New("rendered resources exceed comparison limit")
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	objects := map[string]json.RawMessage{}
	for {
		var encoded json.RawMessage
		if err := decoder.Decode(&encoded); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, errors.New("rendered resources cannot be compared")
		}
		if len(encoded) == 0 {
			continue
		}
		// Keep exact numeric tokens: float64 would collapse distinct large integer
		// fields in custom resources into the same value before comparison.
		var object map[string]any
		jsonDecoder := json.NewDecoder(bytes.NewReader(encoded))
		jsonDecoder.UseNumber()
		if err := jsonDecoder.Decode(&object); err != nil {
			return nil, errors.New("rendered resources cannot be compared")
		}
		if len(object) == 0 {
			continue
		}
		apiVersion, _ := object["apiVersion"].(string)
		kind, _ := object["kind"].(string)
		metadata, _ := object["metadata"].(map[string]any)
		name, _ := metadata["name"].(string)
		namespace, _ := metadata["namespace"].(string)
		annotations, _ := metadata["annotations"].(map[string]any)
		if apiVersion == "" || kind == "" || kind == "List" || name == "" || annotations["helm.sh/hook"] != nil {
			return nil, errors.New("rendered resource has no comparable identity")
		}
		key, _ := json.Marshal([]string{apiVersion, kind, namespace, name})
		if _, duplicate := objects[string(key)]; duplicate {
			return nil, errors.New("duplicate rendered resource")
		}
		raw, err := json.Marshal(object)
		if err != nil {
			return nil, errors.New("rendered resources cannot be compared")
		}
		objects[string(key)] = raw
		if len(objects) > 1000 {
			return nil, errors.New("rendered resources exceed comparison limit")
		}
	}
	if len(objects) == 0 {
		return nil, errors.New("no rendered resources to compare")
	}
	return json.Marshal(objects)
}

// Time/random helpers can return identical results within one clock tick. Avoid
// skipping these charts even if two adjacent renders happen to agree.
var variableHelmFunction = regexp.MustCompile(`\b(now|ago|randAlphaNum|randAlpha|randNumeric|randAscii|randBytes|randInt|uuidv4|shuffle|genCA|genCAWithKey|genSelfSignedCert|genSelfSignedCertWithKey|genSignedCert|genSignedCertWithKey|genPrivateKey|encryptAES|htpasswd|bcrypt)\b`)

func stableHelmChart(c *chart.Chart, values map[string]any) bool {
	if c == nil {
		return false
	}
	usesFileTemplates := false
	for _, file := range c.Templates {
		if variableHelmFunction.Match(file.Data) {
			return false
		}
		usesFileTemplates = usesFileTemplates || bytes.Contains(file.Data, []byte("tpl")) && bytes.Contains(file.Data, []byte(".Files"))
	}
	if usesFileTemplates {
		for _, file := range c.Files {
			if bytes.Contains(file.Data, []byte("{{")) && variableHelmFunction.Match(file.Data) {
				return false
			}
		}
	}
	for _, input := range []map[string]any{values, c.Values} {
		raw, err := json.Marshal(input)
		if err != nil || bytes.Contains(raw, []byte("{{")) && variableHelmFunction.Match(raw) {
			return false
		}
	}
	for _, dependency := range c.Dependencies() {
		if !stableHelmChart(dependency, dependency.Values) {
			return false
		}
	}
	return true
}

func currentComparedRelease(history []*helmrelease.Release, app core.App, server core.Server, previous core.Deployment) *helmrelease.Release {
	var latest *helmrelease.Release
	for _, r := range history {
		if r == nil || r.Info == nil {
			continue
		}
		if latest == nil || r.Version > latest.Version {
			latest = r
		}
	}
	if latest == nil || latest.Info.Status != helmrelease.StatusDeployed || latest.Name != helmReleaseName(app) || latest.Namespace != helmNamespace(app, server) || previous.FinishedAt == nil || !releaseValuesMatch(latest, previous, previous.Snapshot.Values) || len(latest.Hooks) != 0 || latest.Manifest == "" {
		return nil
	}
	return latest
}

func renderHelmEquivalent(ctx context.Context, app core.App, server core.Server, revision string, previous core.Deployment) (bool, error) {
	prepared, cleanup, err := prepareKubernetesServer(server)
	if err != nil {
		return false, errors.New("comparison target unavailable")
	}
	defer cleanup()
	dir, err := os.MkdirTemp("", "dispatch-helm-comparison-")
	if err != nil {
		return false, errors.New("comparison workspace unavailable")
	}
	defer os.RemoveAll(dir)
	if !gitBackedHelmChart(app) {
		return false, nil
	}
	source := filepath.Join(dir, "source")
	if runGitForApp(ctx, app, "clone", "--depth", "1", app.SourceRepo, source) != nil || runGitForApp(ctx, app, "-C", source, "fetch", "--depth", "1", "origin", revision) != nil || runGitForApp(ctx, app, "-C", source, "checkout", "--detach", revision) != nil {
		return false, errors.New("comparison chart source unavailable")
	}
	chartPath, err := within(source, app.HelmChart)
	if err != nil {
		return false, errors.New("comparison chart path invalid")
	}
	// Reject repository symlinks that could cause comparison to read local files.
	resolvedPath, err := filepath.EvalSymlinks(chartPath)
	if err != nil {
		return false, errors.New("comparison chart path unavailable")
	}
	relative, err := filepath.Rel(source, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, errors.New("comparison chart path invalid")
	}
	// Only chart files and its vendored dependencies affect this render. Refuse
	// nested symlinks before Helm's loader can follow them outside this tree.
	if err := filepath.WalkDir(resolvedPath, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("chart contains a symlink")
		}
		return nil
	}); err != nil {
		return false, errors.New("comparison chart paths unavailable")
	}
	client, err := newSDKHelmClient(prepared, helmNamespace(app, server), dir)
	if err != nil {
		return false, errors.New("comparison target unavailable")
	}
	sdk := client.(*sdkHelmClient)
	getter := driftRESTGetter{RESTClientGetter: sdk.settings.RESTClientGetter(), ctx: ctx}
	if sdk.configuration.Init(getter, helmNamespace(app, server), os.Getenv("HELM_DRIVER"), func(string, ...any) {}) != nil {
		return false, errors.New("comparison target unavailable")
	}
	sdk.configuration.RegistryClient = sdk.registry
	history, err := action.NewHistory(sdk.configuration).Run(helmReleaseName(app))
	if err != nil {
		return false, errors.New("comparison release unavailable")
	}
	installed := currentComparedRelease(history, app, server, previous)
	if installed == nil {
		return false, nil
	}
	want, err := canonicalHelmManifest(installed.Manifest)
	if err != nil {
		return false, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		c, err := loader.Load(resolvedPath)
		if err != nil || action.CheckDependencies(c, c.Metadata.Dependencies) != nil {
			return false, errors.New("comparison chart unavailable")
		}
		values, err := helmValues(app)
		if err != nil {
			return false, errors.New("comparison values invalid")
		}
		if !stableHelmChart(c, values) {
			return false, nil
		}
		upgrade := action.NewUpgrade(sdk.configuration)
		upgrade.SetRegistryClient(sdk.registry)
		upgrade.Namespace = helmNamespace(app, server)
		upgrade.ResetValues, upgrade.DisableHooks = true, true
		upgrade.DryRun, upgrade.DryRunOption = true, "server"
		upgrade.Timeout = 30 * time.Second
		rendered, err := upgrade.RunWithContext(ctx, helmReleaseName(app), c, values)
		if err != nil {
			return false, errors.New("comparison rendering unavailable")
		}
		if rendered == nil || len(rendered.Hooks) != 0 || rendered.Version != installed.Version+1 {
			return false, nil
		}
		actual, err := canonicalHelmManifest(rendered.Manifest)
		if err != nil {
			return false, err
		}
		if !bytes.Equal(want, actual) {
			return false, nil
		}
	}
	// A concurrent external Helm operation must not validate a superseded release.
	history, err = action.NewHistory(sdk.configuration).Run(helmReleaseName(app))
	if err != nil {
		return false, errors.New("comparison release unavailable")
	}
	latest := currentComparedRelease(history, app, server, previous)
	if latest == nil || latest.Version != installed.Version {
		return false, nil
	}
	last, err := canonicalHelmManifest(latest.Manifest)
	return err == nil && bytes.Equal(want, last), nil
}

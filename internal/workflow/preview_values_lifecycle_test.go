package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
	"gopkg.in/yaml.v3"
)

func TestPreviewValuesStayFrozenThroughQueuedExecutionAndReloadedApproval(t *testing.T) {
	f := newPreviewValuesLifecycleFixture(t, "same_repository")
	ctx := context.Background()

	// Let the command finish accepting its inputs before the worker can deploy.
	unlock := f.service.lock("resource:" + f.resource.ID)
	var once sync.Once
	release := func() { once.Do(unlock) }
	defer release()
	accepted, err := f.service.Start(ctx, f.resource.ID, "pull request comment 1")
	previewValuesMust(t, err)
	f.replaceValues(t, previewValuesOverlay("blue", false))
	release()
	deployed := f.waitRevision(t, accepted.ID, "awaiting_approval")
	f.assertAppValues(t, "development", "red", true)

	// A new controller must recover the accepted values from SQL, even though
	// the preview's current settings have already changed.
	f.reopen(t)
	saved, err := f.data.GetWorkflowRevision(ctx, accepted.ID)
	previewValuesMust(t, err)
	if !reflect.DeepEqual(saved.PreviewValues, accepted.PreviewValues) || !reflect.DeepEqual(saved.PreviewValues, previewValuesOverlay("red", true)) {
		t.Fatalf("accepted override snapshot changed after reload: %#v", saved.PreviewValues)
	}
	stages, err := f.data.ListWorkflowStageRuns(ctx, deployed.ID)
	previewValuesMust(t, err)
	var approvalID string
	for _, stage := range stages {
		if stage.StageName == "staging" && stage.State == "awaiting_approval" {
			approvalID = stage.ID
		}
	}
	if approvalID == "" {
		t.Fatalf("staging approval was not preserved: %+v", stages)
	}
	_, err = f.service.ApproveStage(ctx, approvalID)
	previewValuesMust(t, err)
	f.waitRevision(t, accepted.ID, "succeeded")
	f.assertAppValues(t, "staging", "red", true)
	current := f.readTrigger(t)
	if !reflect.DeepEqual(current.PreviewValues, previewValuesOverlay("blue", false)) {
		t.Fatalf("approving the saved revision replaced current preview settings: %#v", current.PreviewValues)
	}
}

func TestPreviewValuesReplacementAndClearApplyToFollowingRevisions(t *testing.T) {
	f := newPreviewValuesLifecycleFixture(t, "same_repository")
	ctx := context.Background()
	first, err := f.service.Start(ctx, f.resource.ID, "pull request comment 1")
	previewValuesMust(t, err)
	f.waitRevision(t, first.ID, "awaiting_approval")
	f.assertAppValues(t, "development", "red", true)

	f.replaceValues(t, previewValuesOverlay("blue", false))
	second, err := f.service.Start(ctx, f.resource.ID, "pull request comment 2")
	previewValuesMust(t, err)
	f.waitRevision(t, second.ID, "awaiting_approval")
	f.assertAppValues(t, "development", "blue", false)

	f.replaceValues(t, nil)
	third, err := f.service.Start(ctx, f.resource.ID, "pull request comment 3")
	previewValuesMust(t, err)
	f.waitRevision(t, third.ID, "awaiting_approval")
	f.assertAppValues(t, "development", "default", false)
	if len(third.PreviewValues) != 0 {
		t.Fatalf("cleared overrides survived in the new revision: %#v", third.PreviewValues)
	}
	retained, err := f.data.GetWorkflowRevision(ctx, first.ID)
	previewValuesMust(t, err)
	if !reflect.DeepEqual(retained.PreviewValues, previewValuesOverlay("red", true)) {
		t.Fatalf("later replacements changed the first revision's values: %#v", retained.PreviewValues)
	}
	resource, err := f.data.GetWorkflowResource(ctx, f.resource.ID)
	previewValuesMust(t, err)
	if resource.Document != f.resource.Document || resource.SpecDigest != f.resource.SpecDigest {
		t.Fatal("editing preview values rewrote the application definition")
	}
}

func TestPreviewChecksKeepDeployedValuesAfterPreviewSettingsChange(t *testing.T) {
	f := newPreviewValuesLifecycleFixture(t, "same_repository")
	ctx := context.Background()
	accepted, err := f.service.Start(ctx, f.resource.ID, "pull request comment 1")
	previewValuesMust(t, err)
	deployed := f.waitRevision(t, accepted.ID, "awaiting_approval")
	f.replaceValues(t, previewValuesOverlay("blue", false))
	starts := f.runner.starts.Load()

	checks, err := f.service.StartPreviewChecks(ctx, f.resource.ID, "2")
	previewValuesMust(t, err)
	completed := f.waitRevision(t, checks.ID, "succeeded")
	if !reflect.DeepEqual(completed.PreviewValues, deployed.PreviewValues) || !reflect.DeepEqual(completed.PreviewValues, previewValuesOverlay("red", true)) {
		t.Fatalf("checks used settings that have not been deployed: %#v", completed.PreviewValues)
	}
	if completed.SourceTrust == nil || deployed.SourceTrust == nil || completed.SourceTrust.Digest != deployed.SourceTrust.Digest {
		t.Fatalf("test-only source approval changed with undeployed overrides: deployed=%+v checks=%+v", deployed.SourceTrust, completed.SourceTrust)
	}
	jobs, err := f.data.ListWorkflowJobResults(ctx, checks.ID)
	previewValuesMust(t, err)
	if len(jobs) != 0 || f.runner.starts.Load() != starts {
		t.Fatalf("checks rebuilt or redeployed the preview: jobs=%d deployments=%d", len(jobs), f.runner.starts.Load()-starts)
	}
	stages, err := f.data.ListWorkflowStageRuns(ctx, checks.ID)
	previewValuesMust(t, err)
	if len(stages) != 1 || stages[0].CheckRuns["qa"] == "" || len(stages[0].DeploymentIDs) != 0 {
		t.Fatalf("on-demand checks did not run independently: %+v", stages)
	}
	pipeline, err := f.data.GetWorkflowRevision(ctx, stages[0].CheckRuns["qa"])
	previewValuesMust(t, err)
	if pipeline.State != "succeeded" {
		t.Fatalf("preview verification pipeline did not succeed: %+v", pipeline)
	}
	waiting, err := f.data.GetWorkflowRevision(ctx, deployed.ID)
	previewValuesMust(t, err)
	if waiting.State != "awaiting_approval" {
		t.Fatalf("test-only run changed the waiting deployment: %+v", waiting)
	}
	f.assertAppValues(t, "development", "red", true)
}

func TestPreviewValuesSourceApprovalCoversOnlyItsFrozenSnapshot(t *testing.T) {
	f := newPreviewValuesLifecycleFixture(t, "approval_required")
	ctx := context.Background()

	first, err := f.service.Start(ctx, f.resource.ID, "pull request comment 1")
	if !errors.Is(err, ErrPreviewSourceTrust) || first.SourceTrust == nil || first.SourceTrust.Digest == "" {
		t.Fatalf("strict preview did not require review of the first override snapshot: %+v %v", first.SourceTrust, err)
	}
	now := time.Now().UTC()
	approval := core.PreviewSourceTrustApproval{ID: "preview-values-source-approval", ResourceID: f.resource.ID, Digest: first.SourceTrust.Digest, ActorID: "controller-owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	previewValuesMust(t, f.data.CreatePreviewSourceTrustApproval(ctx, approval))
	f.replaceValues(t, previewValuesOverlay("blue", false))
	decision, err := f.service.SourceTrust(ctx, f.resource, first)
	previewValuesMust(t, err)
	if !decision.Allowed || decision.ApprovalID != approval.ID || decision.Digest != first.SourceTrust.Digest {
		t.Fatalf("changing current settings invalidated the reviewed snapshot: %+v", decision)
	}
	second, err := f.service.Start(ctx, f.resource.ID, "pull request comment 2")
	if !errors.Is(err, ErrPreviewSourceTrust) || second.SourceTrust == nil || second.SourceTrust.Digest == first.SourceTrust.Digest {
		t.Fatalf("different overrides reused source approval: %+v %v", second.SourceTrust, err)
	}
	if f.runner.starts.Load() != 0 {
		t.Fatal("unapproved override inputs reached Helm execution")
	}

	f.replaceValues(t, previewValuesOverlay("red", true))
	accepted, err := f.service.Start(ctx, f.resource.ID, "pull request comment 3")
	previewValuesMust(t, err)
	f.waitRevision(t, accepted.ID, "awaiting_approval")
	f.replaceValues(t, previewValuesOverlay("blue", false))
	stages, err := f.data.ListWorkflowStageRuns(ctx, accepted.ID)
	previewValuesMust(t, err)
	for _, stage := range stages {
		if stage.StageName == "staging" {
			_, err := f.service.ApproveStage(ctx, stage.ID)
			previewValuesMust(t, err)
		}
	}
	f.waitRevision(t, accepted.ID, "succeeded")
	f.assertAppValues(t, "staging", "red", true)
}

type previewValuesLifecycleFixture struct {
	path     string
	data     *store.SQLStore
	service  *Service
	runner   *previewValuesLifecycleRunner
	resource core.WorkflowResource
}

func newPreviewValuesLifecycleFixture(t *testing.T, trustPolicy string) *previewValuesLifecycleFixture {
	t.Helper()
	ctx := context.Background()
	f := &previewValuesLifecycleFixture{path: filepath.Join(t.TempDir(), "preview-values.db")}
	var err error
	f.data, err = store.Open(ctx, f.path)
	previewValuesMust(t, err)
	t.Cleanup(func() { _ = f.data.Close() })
	previewValuesMust(t, f.data.Migrate(ctx))
	now := time.Now().UTC()
	previewValuesMust(t, f.data.CreateProject(ctx, core.Project{ID: "preview-values-project", Name: "Preview values", CreatedAt: now}))
	connection := core.GitHubAppConnection{ID: "preview-values-github", Name: "GitHub", WebURL: "https://github.example.test", State: "ready", CreatedAt: now, UpdatedAt: now}
	previewValuesMust(t, f.data.CreateGitHubApp(ctx, connection))
	source := core.ConfigSource{ID: "preview-values-source", ProjectID: "preview-values-project", GitHubAppID: connection.ID, Name: "Config", Repository: "example/config", Branch: "main", Path: ".dispatch", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	previewValuesMust(t, f.data.CreateConfigSource(ctx, source))
	previewValuesMust(t, f.data.CreateServer(ctx, core.Server{ID: "preview-values-target", Name: "Target", State: "ready", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "fixture-config"}, CreatedAt: now}))

	const document = `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: preview-values
spec:
  sources:
    app:
      repository: example/application
      ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  jobs:
    build:
      runFrom: app
      run: exit 99
      reuse: onInputMatch
      outputs: [image]
  deployments:
    app:
      helm:
        sourceRef: app
        chartPath: charts/app
        namespace: preview-values
        releaseName: preview-values-{{ stage.name }}
        values:
          settings: {color: default, retained: template}
          image: {tag: template-image}
        bindings:
          image.tag: {outputRef: build.image}
  stages:
    - name: development
      targetRef: Target
      approval: automatic
      url: https://preview.example.test/42
      deploy: [app]
      checks:
        qa:
          pipelineRef: preview-values-qa
          when: onDemand
          with: {url: '{{ stage.url }}'}
    - name: staging
      targetRef: Target
      approval: required
      deploy: [app]
`
	docs, err := Parse("preview.yaml", []byte(document))
	previewValuesMust(t, err)
	digest, err := docs[0].Digest()
	previewValuesMust(t, err)
	f.resource = core.WorkflowResource{ID: "preview-values-resource", ConfigSourceID: source.ID, APIVersion: APIVersion, Kind: KindApplication, Name: "preview-values", Path: "preview.yaml", Document: document, SpecDigest: digest, ConfigSHA: "config-sha", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	previewValuesMust(t, f.data.CreateWorkflowResource(ctx, f.resource))
	const pipeline = `apiVersion: dispatch/v1alpha1
kind: Pipeline
metadata:
  name: preview-values-qa
spec:
  inputs:
    url: {type: string, required: true}
  jobs:
    verify:
      run: test "{{ inputs.url }}" = "https://preview.example.test/42"
`
	pipelineDocs, err := Parse("qa.yaml", []byte(pipeline))
	previewValuesMust(t, err)
	pipelineDigest, err := pipelineDocs[0].Digest()
	previewValuesMust(t, err)
	previewValuesMust(t, f.data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "preview-values-qa", ConfigSourceID: source.ID, APIVersion: APIVersion, Kind: KindPipeline, Name: "preview-values-qa", Path: "qa.yaml", Document: pipeline, SpecDigest: pipelineDigest, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	trigger := core.WorkflowPreviewTrigger{ID: "preview-values-trigger", ResourceID: f.resource.ID, GitHubAppID: connection.ID, Repository: "example/application", PullRequestNumber: 42, Command: "/preview", PreviewURL: "https://preview.example.test/42", SourceTrustPolicy: trustPolicy, PreviewValues: previewValuesOverlay("red", true), CreatedAt: now}
	previewValuesMust(t, f.data.CreateWorkflowPreviewTrigger(ctx, trigger))

	// Reusing this build permits real Start calls without fetching a repository.
	// If the cache misses, exit 99 makes the regression fail before deployment.
	pinned := core.WorkflowSourceRevision{Alias: "app", Repository: trigger.Repository, Branch: strings.Repeat("a", 40), CommitSHA: strings.Repeat("a", 40)}
	prior := core.WorkflowRevision{ID: "preview-values-build-baseline", ResourceID: f.resource.ID, ConfigSHA: f.resource.ConfigSHA, SpecDigest: digest, Sources: map[string]core.WorkflowSourceRevision{"app": pinned}, State: "succeeded", CreatedAt: now.Add(-time.Hour), FinishedAt: &now}
	previewValuesMust(t, f.data.CreateWorkflowRevision(ctx, prior))
	fingerprint := jobExecutionFingerprint("build", docs[0].Spec.Jobs["build"], prior.Sources, nil, nil)
	previewValuesMust(t, f.data.CreateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: "preview-values-cached-build", ResourceID: f.resource.ID, RevisionID: prior.ID, JobName: "build", Fingerprint: fingerprint, Sources: prior.Sources, State: "succeeded", Outputs: map[string]string{"image": "built-image"}, CreatedAt: prior.CreatedAt, FinishedAt: &now}))
	f.runner = &previewValuesLifecycleRunner{data: f.data}
	f.resetService()
	return f
}

func (f *previewValuesLifecycleFixture) resetService() {
	f.service = &Service{Store: f.data, Deployments: f.runner}
	f.service.ResolvePreviewSource = func(context.Context, string, string, int) (githubapp.PullRequestHead, error) {
		head := githubapp.PullRequestHead{State: "open"}
		head.Head.SHA = strings.Repeat("a", 40)
		head.Head.Repo = &githubapp.PullRequestRepository{ID: 42, FullName: "example/application"}
		head.Base.Repo = &githubapp.PullRequestRepository{ID: 42, FullName: "example/application"}
		return head, nil
	}
}

func (f *previewValuesLifecycleFixture) reopen(t *testing.T) {
	t.Helper()
	// runApplication releases this lock only after its last revision update.
	unlock := f.service.lock("resource:" + f.resource.ID)
	unlock()
	previewValuesMust(t, f.data.Close())
	var err error
	f.data, err = store.Open(context.Background(), f.path)
	previewValuesMust(t, err)
	f.runner.data = f.data
	f.resetService()
}

func (f *previewValuesLifecycleFixture) readTrigger(t *testing.T) core.WorkflowPreviewTrigger {
	t.Helper()
	triggers, err := f.data.ListWorkflowPreviewTriggers(context.Background())
	previewValuesMust(t, err)
	for _, trigger := range triggers {
		if trigger.ResourceID == f.resource.ID && trigger.ClosedAt == nil {
			return trigger
		}
	}
	t.Fatal("active preview trigger missing")
	return core.WorkflowPreviewTrigger{}
}

func (f *previewValuesLifecycleFixture) replaceValues(t *testing.T, values map[string]map[string]any) {
	t.Helper()
	trigger := f.readTrigger(t)
	trigger.PreviewValues = values
	previewValuesMust(t, f.data.SaveWorkflowPreviewSources(context.Background(), f.resource, trigger))
}

func (f *previewValuesLifecycleFixture) waitRevision(t *testing.T, id, state string) core.WorkflowRevision {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		revision, err := f.data.GetWorkflowRevision(context.Background(), id)
		previewValuesMust(t, err)
		if revision.State == state {
			return revision
		}
		if revision.State == "failed" || revision.State == "cancelled" || revision.State == "succeeded" {
			t.Fatalf("revision reached %s instead of %s: %s", revision.State, state, revision.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("revision %s did not reach %s", id, state)
	return core.WorkflowRevision{}
}

func (f *previewValuesLifecycleFixture) assertAppValues(t *testing.T, stage, color string, extra bool) {
	t.Helper()
	app, err := f.data.GetApp(context.Background(), managedAppID(f.resource.ID, "app", stage))
	previewValuesMust(t, err)
	var values map[string]any
	previewValuesMust(t, yaml.Unmarshal([]byte(app.HelmValues), &values))
	settings, ok := values["settings"].(map[string]any)
	if !ok || settings["color"] != color || settings["retained"] != "template" {
		t.Fatalf("%s deployed unexpected settings: %#v", stage, values)
	}
	_, hasExtra := settings["extra"]
	if hasExtra != extra {
		t.Fatalf("%s retained a replaced override key: %#v", stage, settings)
	}
	image, ok := values["image"].(map[string]any)
	if !ok || image["tag"] != "built-image" {
		t.Fatalf("%s overrides displaced the accepted build output: %#v", stage, values)
	}
}

func previewValuesOverlay(color string, extra bool) map[string]map[string]any {
	settings := map[string]any{"color": color}
	if extra {
		settings["extra"] = "first-revision-only"
	}
	return map[string]map[string]any{"app": {"settings": settings, "image": map[string]any{"tag": "override-image"}}}
}

func previewValuesMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

type previewValuesLifecycleRunner struct {
	data   *store.SQLStore
	starts atomic.Int64
}

func (r *previewValuesLifecycleRunner) Start(ctx context.Context, appID, sha string) (core.Deployment, error) {
	app, err := r.data.GetApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, err
	}
	var values map[string]any
	if err := yaml.Unmarshal([]byte(app.HelmValues), &values); err != nil {
		return core.Deployment{}, err
	}
	now := time.Now().UTC()
	deployment := core.Deployment{ID: fmt.Sprintf("preview-values-deployment-%d", r.starts.Add(1)), AppID: appID, CommitSHA: sha, SpecDigest: app.SpecDigest(), State: core.DeploymentSucceeded, CreatedAt: now, FinishedAt: &now, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: values}}
	if err := r.data.CreateDeployment(ctx, deployment); err != nil {
		return deployment, err
	}
	return r.data.GetDeployment(ctx, deployment.ID)
}

func (r *previewValuesLifecycleRunner) StartIfChanged(ctx context.Context, appID, sha string) (core.Deployment, bool, error) {
	deployment, err := r.Start(ctx, appID, sha)
	return deployment, false, err
}

func (r *previewValuesLifecycleRunner) ReuseCandidateIfUnchanged(context.Context, core.App, string, string) (core.Deployment, bool, error) {
	return core.Deployment{}, false, nil
}

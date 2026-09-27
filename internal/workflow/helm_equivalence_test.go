package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

type workflowDeploymentRecorder struct {
	startState    core.DeploymentState
	data          *store.SQLStore
	unchanged     bool
	compares      int
	starts        int
	automatic     int
	candidates    []core.App
	wantImage     string
	beforeCompare func(core.App)
}

func (r *workflowDeploymentRecorder) Start(ctx context.Context, appID, sha string) (core.Deployment, error) {
	r.starts++
	app, err := r.data.GetApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, err
	}
	now := time.Now().UTC()
	d := core.Deployment{ID: fmt.Sprintf("accepted-%d", r.starts), AppID: appID, State: core.DeploymentSucceeded, SpecDigest: app.SpecDigest(), CommitSHA: sha, CreatedAt: now, FinishedAt: &now, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: map[string]any{"recorded": true}}}
	if r.startState != "" {
		d.State, d.FinishedAt = r.startState, nil
	}
	if err := r.data.CreateDeployment(ctx, d); err != nil {
		return d, err
	}
	return r.data.GetDeployment(ctx, d.ID)
}
func (r *workflowDeploymentRecorder) StartIfChanged(ctx context.Context, appID, sha string) (core.Deployment, bool, error) {
	r.automatic++
	app, err := r.data.GetApp(ctx, appID)
	if err != nil {
		return core.Deployment{}, false, err
	}
	d, unchanged, err := r.ReuseCandidateIfUnchanged(ctx, app, sha, app.SpecDigest())
	if err != nil || unchanged {
		return d, unchanged, err
	}
	d, err = r.Start(ctx, appID, sha)
	return d, false, err
}
func (r *workflowDeploymentRecorder) ReuseCandidateIfUnchanged(ctx context.Context, candidate core.App, sha, expectedStoredSpecDigest string) (core.Deployment, bool, error) {
	r.compares++
	r.candidates = append(r.candidates, candidate)
	if !r.unchanged || !strings.Contains(candidate.HelmValues, r.wantImage) {
		return core.Deployment{}, false, nil
	}
	if r.beforeCompare != nil {
		r.beforeCompare(candidate)
	}
	original, err := r.data.GetApp(ctx, candidate.ID)
	if err != nil {
		return core.Deployment{}, false, err
	}
	if original.SpecDigest() != expectedStoredSpecDigest || original.ServerID != candidate.ServerID || original.ProjectID != candidate.ProjectID || original.Name != candidate.Name {
		return core.Deployment{}, false, nil
	}
	server, err := r.data.GetServer(ctx, original.ServerID)
	if err != nil {
		return core.Deployment{}, false, err
	}
	d, err := r.data.LatestSuccessfulDeployment(ctx, candidate.ID)
	if err != nil {
		return d, false, err
	}
	proof := core.HelmEquivalence{AppID: candidate.ID, ProjectID: candidate.ProjectID, AppName: candidate.Name, AppSpecDigest: original.SpecDigest(), CandidateSpecDigest: candidate.SpecDigest(), ChartCommit: sha, ServerID: server.ID, TargetDigest: core.HelmTargetDigest(server), DeploymentID: d.ID, CheckedAt: time.Now().UTC()}
	err = r.data.SaveHelmEquivalence(ctx, proof)
	return d, err == nil, err
}

type workflowNoopFixture struct {
	service  *Service
	data     *store.SQLStore
	runner   *workflowDeploymentRecorder
	source   core.ConfigSource
	resource core.WorkflowResource
	document Document
	previous core.WorkflowRevision
	snapshot map[string]core.WorkflowSourceRevision
	app      core.App
	server   core.Server
	baseline core.Deployment
}

func newWorkflowNoopFixture(t *testing.T) workflowNoopFixture {
	t.Helper()
	ctx := context.Background()
	data, source, resource := workflowRunnerFixture(t, "equivalent")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC().Add(-time.Hour)
	server := core.Server{ID: "equivalent-target", Name: "Target", State: "ready", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "fixture-config"}, CreatedAt: now}
	must(data.CreateServer(ctx, server))
	raw := `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: equivalent
spec:
  sources:
    gitops:
      repository: example/gitops
    service:
      repository: example/service
  jobs:
    build:
      runFrom: service
      run: exit 99
      reuse: onInputMatch
      outputs: [image]
  deployments:
    app:
      helm:
        sourceRef: gitops
        chartPath: charts/app
        namespace: application
        releaseName: application
        values:
          image: {tag: from-values-file}
        bindings:
          image.tag: {outputRef: build.image}
  stages:
    - name: development
      targetRef: Target
      approval: automatic
      deploy: [app]
`
	docs, err := Parse(resource.Path, []byte(raw))
	must(err)
	document := docs[0]
	encoded, err := document.MarshalYAML()
	must(err)
	resource.Document = string(encoded)
	resource.SpecDigest, err = document.Digest()
	must(err)
	must(data.UpdateWorkflowResource(ctx, resource))
	sources := map[string]core.WorkflowSourceRevision{
		"gitops":  {Alias: "gitops", Repository: "example/gitops", Branch: "main", CommitSHA: strings.Repeat("a", 40), ContentHashes: map[string]string{"charts/app": "old-chart"}},
		"service": {Alias: "service", Repository: "example/service", Branch: "main", CommitSHA: strings.Repeat("b", 40)},
	}
	previous := core.WorkflowRevision{ID: "baseline-workflow", ResourceID: resource.ID, State: "succeeded", ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest, Sources: sources, Outputs: map[string]map[string]string{"build": {"image": "built-image"}}, CreatedAt: now, FinishedAt: &now}
	must(data.CreateWorkflowRevision(ctx, previous))
	job := document.Spec.Jobs["build"]
	fingerprint := jobExecutionFingerprint("build", job, map[string]core.WorkflowSourceRevision{"service": sources["service"]}, nil, nil)
	must(data.CreateWorkflowJobResult(ctx, core.WorkflowJobResult{ID: "baseline-build", ResourceID: resource.ID, RevisionID: previous.ID, JobName: "build", Fingerprint: fingerprint, State: "succeeded", Sources: map[string]core.WorkflowSourceRevision{"service": sources["service"]}, Outputs: previous.Outputs["build"], CreatedAt: now, FinishedAt: &now}))
	runner := &workflowDeploymentRecorder{data: data, unchanged: true, wantImage: "built-image"}
	s := &Service{Store: data, Deployments: runner}
	prepared, err := s.prepareHelmDeployment(ctx, resource, source, previous, document.Spec.Stages[0], "app", document.Spec.Deployments["app"], server)
	must(err)
	must(data.CreateApp(ctx, prepared.app))
	baseline := core.Deployment{ID: "baseline-deployment", AppID: prepared.app.ID, State: core.DeploymentSucceeded, SpecDigest: prepared.app.SpecDigest(), CommitSHA: sources["gitops"].CommitSHA, CreatedAt: now, FinishedAt: &now, Snapshot: core.DeploymentSnapshot{TargetID: server.ID, Runtime: server.Runtime, Namespace: "application", Release: "application", ValueSources: map[string]string{"image.tag": "original provenance"}}}
	must(data.CreateDeployment(ctx, baseline))
	must(data.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: baseline.ID, Level: "info", Message: "Original deployment evidence", CreatedAt: now}))
	currentGitops := sources["gitops"]
	currentGitops.CommitSHA, currentGitops.ContentHashes = strings.Repeat("c", 40), map[string]string{"charts/app": "changed-chart"}
	snapshot := map[string]core.WorkflowSourceRevision{"gitops": currentGitops, "service": sources["service"]}
	return workflowNoopFixture{s, data, runner, source, resource, document, previous, snapshot, prepared.app, server, baseline}
}

func TestEquivalentAutomaticWorkflowCreatesObservationWithoutHistory(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		trigger := []string{"poll", "github push", "configuration sync"}[i%3]
		wg.Add(1)
		go func() {
			defer wg.Done()
			revision, err := f.service.startIfChanged(ctx, f.resource, f.source, f.snapshot, trigger)
			if err != nil || revision.ID != "" {
				t.Errorf("equivalent automatic inputs created execution: %s %v", revision.ID, err)
			}
		}()
	}
	wg.Wait()
	if f.runner.compares != 1 || f.runner.starts != 0 {
		t.Fatal("equivalence observation was not reused", f.runner.compares, f.runner.starts)
	}
	observed, err := f.data.GetWorkflowEquivalence(ctx, f.resource.ID)
	if err != nil || observed.Sources["gitops"].CommitSHA != f.snapshot["gitops"].CommitSHA || len(observed.Results) != 1 || observed.Results[0].DeploymentID != f.baseline.ID {
		t.Fatal("fresh evaluated source observation missing", observed, err)
	}
	workflows, _ := f.data.ListWorkflowRevisions(ctx, f.resource.ID, 100)
	deployments, _ := f.data.ListDeployments(ctx, 100)
	jobs, _ := f.data.ListWorkflowJobResults(ctx, f.previous.ID)
	if len(workflows) != 1 || len(deployments) != 1 || len(jobs) != 1 {
		t.Fatal("unchanged evaluation added historical execution rows")
	}
	app, _ := f.data.GetApp(ctx, f.app.ID)
	baseline, _ := f.data.GetDeployment(ctx, f.baseline.ID)
	logs, _ := f.data.ListDeploymentLogs(ctx, f.baseline.ID, 0)
	if app.SpecDigest() != f.app.SpecDigest() || baseline.CommitSHA != f.baseline.CommitSHA || !reflect.DeepEqual(baseline.Snapshot.ValueSources, f.baseline.Snapshot.ValueSources) || len(logs) != 1 || logs[0].Message != "Original deployment evidence" {
		t.Fatal("comparison mutated retained deployment or saved application")
	}
	if !strings.Contains(f.runner.candidates[0].HelmValues, "built-image") || strings.Contains(f.runner.candidates[0].HelmValues, "from-values-file") {
		t.Fatal("comparison ran before build outputs were applied")
	}
}

func TestWorkflowEquivalencePreservesExecutionBoundaries(t *testing.T) {
	for _, name := range []string{"approval", "checks", "finally", "never-reuse", "changed-job-source", "changed-output", "changed-render", "changed-definition", "running", "missing-output", "active-execution"} {
		t.Run(name, func(t *testing.T) {
			f := newWorkflowNoopFixture(t)
			ctx := context.Background()
			var unlock func()
			switch name {
			case "approval":
				f.document.Spec.Stages[0].Approval = "required"
			case "checks":
				f.document.Spec.Stages[0].Checks = map[string]CheckSpec{"test": {PipelineRef: "smoke"}}
			case "finally":
				f.document.Spec.Finally = map[string]JobSpec{"cleanup": {RunFrom: "service", Run: "cleanup"}}
			case "never-reuse":
				job := f.document.Spec.Jobs["build"]
				job.Reuse = "never"
				f.document.Spec.Jobs["build"] = job
			case "changed-job-source":
				source := f.snapshot["service"]
				source.CommitSHA = strings.Repeat("d", 40)
				f.snapshot["service"] = source
			case "changed-output", "missing-output":
				results, _ := f.data.ListWorkflowJobResults(ctx, f.previous.ID)
				results[0].Outputs = map[string]string{}
				if name == "changed-output" {
					results[0].Outputs["image"] = "different-build-image"
				}
				if err := f.data.UpdateWorkflowJobResult(ctx, results[0]); err != nil {
					t.Fatal(err)
				}
			case "changed-render":
				f.runner.unchanged = false
			case "changed-definition":
				f.resource.SpecDigest = "changed-definition"
			case "running":
				f.previous.State = "running"
				if err := f.data.UpdateWorkflowRevision(ctx, f.previous); err != nil {
					t.Fatal(err)
				}
			case "active-execution":
				unlock = f.service.lock("resource:" + f.resource.ID)
				defer unlock()
			}
			encoded, err := f.document.MarshalYAML()
			if err != nil {
				t.Fatal(err)
			}
			f.resource.Document = string(encoded)
			if f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
				t.Fatal("execution obligation was skipped")
			}
		})
	}
}

func TestAutomaticStageReusesDeploymentAndManualStageDeploys(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	for _, trigger := range []string{"poll", "manual"} {
		revision := core.WorkflowRevision{ID: trigger + "-workflow", ResourceID: f.resource.ID, State: "running", Trigger: trigger, SpecDigest: f.resource.SpecDigest, Sources: f.snapshot, Outputs: f.previous.Outputs, CreatedAt: time.Now().UTC()}
		if err := f.data.CreateWorkflowRevision(ctx, revision); err != nil {
			t.Fatal(err)
		}
		stage := f.document.Spec.Stages[0]
		run := core.WorkflowStageRun{ID: trigger + "-stage", RevisionID: revision.ID, StageName: stage.Name, TargetRef: stage.TargetRef, State: "running", CheckRuns: map[string]string{}, CreatedAt: time.Now().UTC()}
		if err := f.data.CreateWorkflowStageRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := f.service.deployStage(ctx, f.resource, f.source, f.document, revision, stage, &run); err != nil {
			t.Fatal(err)
		}
		if len(run.DeploymentIDs) != 1 || len(run.DeploymentResults) != 1 {
			t.Fatal("stage omitted deployment decision")
		}
		if trigger == "poll" && (run.DeploymentIDs[0] != f.baseline.ID || run.DeploymentResults[0].Outcome != "unchanged") {
			t.Fatal("automatic stage did not reference retained deployment")
		}
		if trigger == "manual" && (run.DeploymentIDs[0] == f.baseline.ID || run.DeploymentResults[0].Outcome != "deployed") {
			t.Fatal("manual stage did not create explicit deployment")
		}
	}
	if f.runner.automatic != 1 || f.runner.starts != 1 {
		t.Fatal("manual and automatic acceptance paths were not distinct")
	}
	logs, _ := f.data.ListDeploymentLogs(ctx, f.baseline.ID, 0)
	baseline, _ := f.data.GetDeployment(ctx, f.baseline.ID)
	if len(logs) != 1 || !reflect.DeepEqual(baseline.Snapshot.ValueSources, f.baseline.Snapshot.ValueSources) {
		t.Fatal("stage reuse altered historical logs/provenance")
	}
}

func TestWorkflowEquivalenceCacheRejectsChangedApplication(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	if !f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("initial equivalent evaluation failed")
	}
	app := f.app
	app.PreDeployHook = "intentional hook"
	if err := f.data.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if _, err := f.data.GetWorkflowEquivalence(ctx, f.resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("cached equivalence survived application edit", err)
	}
	if f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("new hook was skipped by equivalence cache")
	}
}

func TestWorkflowEquivalenceCacheResolvesTargetNamesAgain(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	if !f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("initial equivalent evaluation failed")
	}
	original := f.server
	original.Name = "Previous target"
	if err := f.data.UpdateServer(ctx, original); err != nil {
		t.Fatal(err)
	}
	replacement := f.server
	replacement.ID = "replacement-target"
	if err := f.data.CreateServer(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	if f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("cached observation ignored target-name reassignment")
	}
	if f.runner.candidates[len(f.runner.candidates)-1].ServerID != replacement.ID {
		t.Fatal("candidate did not resolve the current target")
	}
}

func TestWorkflowEquivalenceRejectsApplicationEditAfterPreparation(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	f.runner.beforeCompare = func(candidate core.App) {
		app, err := f.data.GetApp(ctx, candidate.ID)
		if err != nil {
			t.Fatal(err)
		}
		app.HelmGroupValues = "inheritedValue: edited-during-comparison\n"
		if err := f.data.UpdateApp(ctx, app); err != nil {
			t.Fatal(err)
		}
	}
	if f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("prepared candidate ignored a concurrent application edit")
	}
	if f.runner.compares != 1 {
		t.Fatal("fixture did not reach candidate comparison")
	}
	if _, err := f.data.GetWorkflowEquivalence(ctx, f.resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("stale prepared candidate created a reusable observation", err)
	}
}

func TestWorkflowEquivalenceCacheRechecksResolvedJobSecrets(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	keyPath := filepath.Join(t.TempDir(), "master.key")
	must(os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0600))
	vault, err := secretcrypto.OpenFile(keyPath)
	must(err)
	f.service.Secrets = secretvalue.New(f.data, vault)
	encrypted, err := vault.Encrypt("secret:build-token", []byte("original-build-secret"))
	must(err)
	secret := core.Secret{ID: "build-token", Name: "build-token", Type: core.SecretTypeAPIToken, EncryptedValue: encrypted, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	must(f.data.CreateSecret(ctx, secret))
	job := f.document.Spec.Jobs["build"]
	job.Secrets = map[string]SecretBinding{"TOKEN": {SecretRef: secret.ID}}
	f.document.Spec.Jobs["build"] = job
	raw, err := f.document.MarshalYAML()
	must(err)
	f.resource.Document = string(raw)
	f.resource.SpecDigest, err = f.document.Digest()
	must(err)
	must(f.data.UpdateWorkflowResource(ctx, f.resource))
	results, err := f.data.ListWorkflowJobResults(ctx, f.previous.ID)
	must(err)
	// Captured definitions and job fingerprints stay immutable. Seed a later
	// successful revision with the secret-bound build result.
	f.previous.ID = "authenticated-workflow"
	f.previous.SpecDigest = f.resource.SpecDigest
	f.previous.CreatedAt = time.Now().UTC().Add(-time.Minute)
	must(f.data.CreateWorkflowRevision(ctx, f.previous))
	result := results[0]
	result.ID, result.RevisionID = "authenticated-result", f.previous.ID
	result.Fingerprint = jobExecutionFingerprint("build", job, result.Sources, nil, map[string]string{"TOKEN": "original-build-secret"})
	must(f.data.CreateWorkflowJobResult(ctx, result))
	if !f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("matching secret-bound build could not be reused")
	}
	compares := f.runner.compares
	secret.EncryptedValue, err = vault.Encrypt("secret:build-token", []byte("rotated-build-secret"))
	must(err)
	must(f.data.UpdateSecret(ctx, secret))
	if f.service.reuseEquivalentWorkflow(ctx, f.resource, f.source, f.snapshot) {
		t.Fatal("cached observation suppressed work after job secret rotation")
	}
	if f.runner.compares != compares {
		t.Fatal("changed job credentials reached Helm comparison before build validation")
	}
}

func TestStageLinksDeploymentBeforeWaiting(t *testing.T) {
	f := newWorkflowNoopFixture(t)
	f.runner.startState = core.DeploymentQueued
	ctx := context.Background()
	revision := core.WorkflowRevision{ID: "in-progress", ResourceID: f.resource.ID, State: "running", Trigger: "manual", Sources: f.snapshot, Outputs: f.previous.Outputs, CreatedAt: time.Now().UTC()}
	if err := f.data.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	stage := f.document.Spec.Stages[0]
	run := core.WorkflowStageRun{ID: "live-stage", RevisionID: revision.ID, StageName: stage.Name, State: "running", CreatedAt: time.Now().UTC()}
	if err := f.data.CreateWorkflowStageRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.service.deployStage(work, f.resource, f.source, f.document, revision, stage, &run) }()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("running deployment was not linked")
		case <-ticker.C:
			saved, err := f.data.GetWorkflowStageRun(ctx, "live-stage")
			if err != nil {
				t.Fatal(err)
			}
			if len(saved.DeploymentIDs) == 0 {
				continue
			}
			deployment, err := f.data.GetDeployment(ctx, saved.DeploymentIDs[0])
			if err != nil {
				t.Fatal(err)
			}
			if deployment.State != core.DeploymentQueued {
				t.Fatalf("state = %s", deployment.State)
			}
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
			return
		}
	}
}

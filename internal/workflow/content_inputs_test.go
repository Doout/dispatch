package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/secretvalue"
	"github.com/doout/dispatch/internal/store"
)

func TestSlotContentChanges(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
	if err := runGit(ctx, env, "init", "--initial-branch=main", repo); err != nil {
		t.Fatal(err)
	}
	write := func(name, contents string) {
		t.Helper()
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() string {
		t.Helper()
		if err := runGit(ctx, env, "-C", repo, "add", "."); err != nil {
			t.Fatal(err)
		}
		if err := runGit(ctx, env, "-C", repo, "commit", "-m", "Update inputs"); err != nil {
			t.Fatal(err)
		}
		return gitOutput(t, repo, "rev-parse", "HEAD")
	}
	write("scripts/build.sh", "echo build")
	write("values/common.yaml", "replicas: 1")
	write("values/slot1.yaml", "name: slot1")
	write("dispatch/slot1.yaml", "slot1")
	first := commit()
	cache := newRepositoryCache(t.TempDir())
	job := JobSpec{RunFrom: "config", Reuse: "onInputMatch", Run: "sh scripts/build.sh", SourcePaths: map[string][]string{"config": {"scripts"}}}
	spec := ApplicationSpec{Jobs: map[string]JobSpec{"build": job}, Deployments: map[string]DeploymentSpec{"app": {Helm: HelmDeploymentSpec{SourceRef: "chart", ValuesFiles: []HelmValuesFile{{SourceRef: "config", Path: "values/common.yaml"}, {SourceRef: "config", Path: "values/slot1.yaml"}}}}}}
	paths := applicationInputPaths(spec)["config"]
	hash := func(sha string) core.WorkflowSourceRevision {
		t.Helper()
		hashes, err := cache.contentHashes(ctx, repo, "test", "main", sha, "", paths, env)
		if err != nil {
			t.Fatal(err)
		}
		return core.WorkflowSourceRevision{Alias: "config", Repository: repo, Branch: "main", CommitSHA: sha, ContentHashes: hashes}
	}
	before := hash(first)
	write("dispatch/slot2.yaml", "slot2")
	write("values/slot2.yaml", "name: slot2")
	added := hash(commit())
	if !sameSourceInput(before, added) {
		t.Fatal("adding a slot invalidated existing slot inputs")
	}
	fingerprint := func(s core.WorkflowSourceRevision) string {
		return jobFingerprint("build", job, map[string]core.WorkflowSourceRevision{"config": s}, nil)
	}
	if fingerprint(before) != fingerprint(added) {
		t.Fatal("configuration-only change invalidated the build")
	}
	write("values/slot1.yaml", "name: changed-slot1")
	valuesChanged := hash(commit())
	if sameSourceInput(added, valuesChanged) {
		t.Fatal("changed Helm values did not invalidate deployment")
	}
	if fingerprint(added) != fingerprint(valuesChanged) {
		t.Fatal("changed Helm values invalidated a build that does not read them")
	}
	write("scripts/build.sh", "echo changed")
	scriptChanged := hash(commit())
	if sameSourceInput(valuesChanged, scriptChanged) || fingerprint(valuesChanged) == fingerprint(scriptChanged) {
		t.Fatal("changed build script was ignored")
	}
	if err := os.Chmod(filepath.Join(repo, "scripts/build.sh"), 0700); err != nil {
		t.Fatal(err)
	}
	modeChanged := hash(commit())
	if fingerprint(scriptChanged) == fingerprint(modeChanged) {
		t.Fatal("executable bit change was ignored")
	}
	if _, err := cache.contentHashes(ctx, repo, "test", "main", first, "", []string{"missing"}, env); err == nil {
		t.Fatal("missing input was silently accepted")
	}
	// Historical revisions are immutable even after the branch moves.
	if got := hash(first); !reflect.DeepEqual(got, before) {
		t.Fatal("historical input hash changed")
	}
	// Enabling scoped inputs on a newly added slot can adopt an older successful
	// build. The deliberately failing command proves that no rebuild happens.
	data, config, original := workflowRunnerFixture(t, "legacy-content")
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef0123456789abcde!"), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.Encrypt("secret:checkout", []byte("test-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	if err := data.CreateSecret(ctx, core.Secret{ID: "checkout", Name: "checkout", Type: core.SecretTypeGitHubToken, EncryptedValue: encrypted, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	config.GitHubAppID = ""
	config.CredentialSecretID = "checkout"
	service := NewService(data, nil, secretvalue.New(data, vault), nil, nil, t.TempDir())
	old := before
	old.ContentHashes = nil
	old.Repository = "file://" + repo
	current := valuesChanged
	current.Repository = old.Repository
	job.Run = "exit 99"
	job.Outputs = []string{"image"}
	legacy := job
	legacy.SourcePaths = nil
	previous := core.WorkflowRevision{ID: "old-build", ResourceID: original.ID, State: "succeeded", Sources: map[string]core.WorkflowSourceRevision{"config": old}, CreatedAt: time.Now().UTC()}
	if err := data.CreateWorkflowRevision(ctx, previous); err != nil {
		t.Fatal(err)
	}
	result := core.WorkflowJobResult{ID: "successful-old-build", ResourceID: original.ID, RevisionID: previous.ID, JobName: "build", State: "succeeded", Sources: previous.Sources, Outputs: map[string]string{"image": "built-once"}, Fingerprint: jobExecutionFingerprint("build", legacy, previous.Sources, nil, nil), CreatedAt: time.Now().UTC()}
	if err := data.CreateWorkflowJobResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	slot := original
	slot.ID = "new-content-slot"
	slot.Name = slot.ID
	if err := data.CreateWorkflowResource(ctx, slot); err != nil {
		t.Fatal(err)
	}
	revision := core.WorkflowRevision{ID: "new-slot-run", ResourceID: slot.ID, State: "running", Sources: map[string]core.WorkflowSourceRevision{"config": current}, CreatedAt: time.Now().UTC()}
	if err := data.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	runtime := jobRuntime{service: service, source: config, revision: revision}
	outputs, err := runtime.executeJob(ctx, slot, "build", job, false)
	if err != nil || outputs["image"] != "built-once" {
		t.Fatalf("old build was not reused: %v %v", outputs, err)
	}
	results, err := data.ListWorkflowJobResults(ctx, revision.ID)
	if err != nil || len(results) != 1 || results[0].ReusedFromID != result.ID {
		t.Fatalf("missing reuse provenance: %v %v", results, err)
	}

	changedInputs := scriptChanged
	changedInputs.Repository = old.Repository
	changedSources := map[string]core.WorkflowSourceRevision{"config": changedInputs}
	_, err = runtime.findContentMatch(ctx, slot, "build", job, changedSources, nil, jobExecutionFingerprint("build", job, changedSources, nil, nil))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("changed script reused a legacy build: %v", err)
	}
	changedSecrets := map[string]string{"TOKEN": "rotated"}
	_, err = runtime.findContentMatch(ctx, slot, "build", job, revision.Sources, changedSecrets, jobExecutionFingerprint("build", job, revision.Sources, nil, changedSecrets))
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("changed credentials reused a legacy build: %v", err)
	}

}

func TestContentSnapshotsSkipExistingSlotsAndStartNewOnes(t *testing.T) {
	ctx := context.Background()
	data, config, resource := workflowRunnerFixture(t, "content-skip")
	service := &Service{Store: data}
	old := core.WorkflowSourceRevision{Alias: "config", Repository: "example/config", Branch: "main", CommitSHA: "old", ContentHashes: map[string]string{"scripts": "same"}}
	previous := core.WorkflowRevision{ID: "prior-content", ResourceID: resource.ID, State: "succeeded", SpecDigest: resource.SpecDigest, Sources: map[string]core.WorkflowSourceRevision{"config": old}, CreatedAt: time.Now().UTC()}
	if err := data.CreateWorkflowRevision(ctx, previous); err != nil {
		t.Fatal(err)
	}
	current := old
	current.CommitSHA = "new"
	snapshot := map[string]core.WorkflowSourceRevision{"config": current}
	if service.snapshotChanged(ctx, resource, snapshot) {
		t.Fatal("unchanged content started another revision")
	}
	// Overlapping event sources must also remain a no-op.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			revision, err := service.startIfChanged(ctx, resource, config, snapshot, "sync")
			if err != nil || revision.ID != "" {
				t.Errorf("unchanged work scheduled: %v %v", revision.ID, err)
			}
		}()
	}
	wg.Wait()
	revisions, err := data.ListWorkflowRevisions(ctx, resource.ID, 20)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("unexpected revisions: %d %v", len(revisions), err)
	}
	fresh := resource
	fresh.ID = "new-slot"
	if !service.snapshotChanged(ctx, fresh, snapshot) {
		t.Fatal("new slot was skipped")
	}
	changed := resource
	changed.SpecDigest = "changed-target"
	if !service.snapshotChanged(ctx, changed, snapshot) {
		t.Fatal("changed deployment configuration was skipped")
	}
	if moved, found, err := existingApplication([]core.WorkflowResource{resource}, "new.yaml", resource.Kind, resource.Name); err != nil || !found || moved.ID != resource.ID {
		t.Fatal("moving a config file discarded slot identity")
	}
}

func TestSourcePathValidationAndConservativeMatching(t *testing.T) {
	job := JobSpec{RunFrom: "app", Run: "true", SourcePaths: map[string][]string{"app": {"scripts"}}}
	for _, bad := range []string{"../outside", "/absolute", "", "**/*.go", "a/../../b"} {
		job.SourcePaths["app"] = []string{bad}
		if err := validateJobs("jobs", map[string]JobSpec{"build": job}, map[string]SourceSpec{"app": {}}, nil); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	job.SourcePaths = map[string][]string{"unknown": {"scripts"}}
	if err := validateJobs("jobs", map[string]JobSpec{"build": job}, map[string]SourceSpec{"app": {}}, nil); err == nil {
		t.Fatal("accepted undeclared input")
	}
	job.SourcePaths = map[string][]string{"app": {"scripts"}}
	spec := ApplicationSpec{Jobs: map[string]JobSpec{"build": job, "other": {RunFrom: "app", Run: "true"}}}
	if !reflect.DeepEqual(applicationInputPaths(spec)["app"], []string{".", "scripts"}) {
		t.Fatal("unscoped job did not retain whole-source dependency")
	}
	job.Run = "echo {{ sources.app.commit }}"
	spec.Jobs = map[string]JobSpec{"build": job}
	if _, ok := applicationInputPaths(spec)["app"]; ok {
		t.Fatal("commit-dependent command was content-matched")
	}
	a := core.WorkflowSourceRevision{CommitSHA: "old"}
	b := a
	b.CommitSHA = "new"
	if sameSourceInput(a, b) {
		t.Fatal("legacy snapshots without hashes were treated as equal")
	}
}

func TestAddingInputDeclarationsDoesNotChangeDeployment(t *testing.T) {
	docs, err := Parse("app.yaml", []byte(`apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: app
spec:
  sources:
    config:
      repository: example/config
  jobs:
    build:
      runFrom: config
      run: echo build
`))
	if err != nil {
		t.Fatal(err)
	}
	doc := docs[0]
	prior, err := doc.Digest()
	if err != nil {
		t.Fatal(err)
	}
	job := doc.Spec.Jobs["build"]
	job.SourcePaths = map[string][]string{"config": {"shared"}}
	doc.Spec.Jobs["build"] = job
	contents, err := doc.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	resource := core.WorkflowResource{Path: "app.yaml", Document: string(contents)}
	if !addsOnlyInputPaths(resource, prior) {
		t.Fatal("input declarations alone changed deployment")
	}
	job.Run = "echo changed"
	doc.Spec.Jobs["build"] = job
	contents, _ = doc.MarshalYAML()
	resource.Document = string(contents)
	if addsOnlyInputPaths(resource, prior) {
		t.Fatal("changed command was ignored")
	}
}

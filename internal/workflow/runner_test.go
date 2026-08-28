package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestRenderRuntimeReferences(t *testing.T) {
	sources := map[string]core.WorkflowSourceRevision{
		"ui": {Alias: "ui", Branch: "main", CommitSHA: "abc123"},
	}
	stage := StageSpec{Name: "development", URL: "https://preview.example.com"}
	got, err := renderRuntime(`./build "{{ sources.ui.path }}" {{ sources.ui.commit }} {{ stage.url }}`, map[string]string{"ui": "/tmp/ui"}, sources, nil, &stage)
	if err != nil {
		t.Fatal(err)
	}
	want := `./build "/tmp/ui" abc123 https://preview.example.com`
	if got != want {
		t.Fatalf("rendered command = %q, want %q", got, want)
	}
	if _, err := renderRuntime("{{ sources.missing.path }}", nil, sources, nil, nil); err == nil {
		t.Fatal("expected missing source error")
	}
}

func TestReadJobOutputs(t *testing.T) {
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"json":   `{"image":"registry/app","tag":"sha-123","ignored":"value"}`,
		"dotenv": "image=registry/app\nexport tag='sha-123'\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, name)
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			outputs, err := readJobOutputs(path, []string{"image", "tag"})
			if err != nil {
				t.Fatal(err)
			}
			if outputs["image"] != "registry/app" || outputs["tag"] != "sha-123" || len(outputs) != 2 {
				t.Fatalf("unexpected outputs: %#v", outputs)
			}
		})
	}
	if _, err := readJobOutputs(filepath.Join(directory, "missing"), []string{"image"}); err == nil {
		t.Fatal("expected missing output file error")
	}
}

func TestJobFingerprintTracksDeclaredSources(t *testing.T) {
	job := JobSpec{RunFrom: "ui", Run: "./build.sh", Outputs: []string{"image"}, Reuse: "onInputMatch"}
	base := map[string]core.WorkflowSourceRevision{"ui": {Alias: "ui", Repository: "owner/ui", Branch: "main", CommitSHA: "one"}}
	changed := map[string]core.WorkflowSourceRevision{"ui": {Alias: "ui", Repository: "owner/ui", Branch: "main", CommitSHA: "two"}}
	first := jobFingerprint("build-ui", job, base, nil)
	second := jobFingerprint("build-ui", job, changed, nil)
	if first == second {
		t.Fatal("source change did not alter the fingerprint")
	}
	if first != jobFingerprint("build-ui", job, base, nil) {
		t.Fatal("fingerprint is not stable")
	}
}

func TestExecuteJobReusesMatchingResult(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "reuse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "project", Name: "Project", CreatedAt: now}
	connection := core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", AppID: 1, InstallationID: 2, WebhookURL: "https://dispatch.example/hook", EncryptedPrivateKey: "key", EncryptedWebhookSecret: "secret", State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	config := core.ConfigSource{ID: "config", ProjectID: project.ID, GitHubAppID: connection.ID, Name: "Config", Repository: "owner/config", Branch: "main", Path: ".dispatch", SyncMode: core.ConfigSyncPoll, PollIntervalSeconds: 300, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConfigSource(ctx, config); err != nil {
		t.Fatal(err)
	}
	resource := core.WorkflowResource{ID: "resource", ConfigSourceID: config.ID, APIVersion: APIVersion, Kind: KindApplication, Name: "app", Path: ".dispatch/app.yaml", Document: "test", SpecDigest: "sha256:spec", ConfigSHA: "config-sha", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	sources := map[string]core.WorkflowSourceRevision{"ui": {Alias: "ui", Repository: "owner/ui", Branch: "main", CommitSHA: "abc123"}}
	previous := core.WorkflowRevision{ID: "previous", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest, State: "succeeded", Trigger: "push", Sources: sources, Outputs: map[string]map[string]string{}, CreatedAt: now}
	current := core.WorkflowRevision{ID: "current", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest, State: "running", Trigger: "push", Sources: sources, Outputs: map[string]map[string]string{}, CreatedAt: now.Add(time.Second)}
	if err := data.CreateWorkflowRevision(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateWorkflowRevision(ctx, current); err != nil {
		t.Fatal(err)
	}
	job := JobSpec{RunFrom: "ui", Run: "exit 99", Outputs: []string{"image"}, Reuse: "onInputMatch"}
	fingerprint := jobFingerprint("build-ui", job, sources, nil)
	priorResult := core.WorkflowJobResult{ID: "prior-job", ResourceID: resource.ID, RevisionID: previous.ID, JobName: "build-ui", Fingerprint: fingerprint, State: "succeeded", Sources: sources, Outputs: map[string]string{"image": "registry/ui:abc123"}, CreatedAt: now}
	if err := data.CreateWorkflowJobResult(ctx, priorResult); err != nil {
		t.Fatal(err)
	}
	runtime := &jobRuntime{service: &Service{Store: data}, source: config, revision: current, root: t.TempDir(), paths: map[string]string{}}
	outputs, err := runtime.executeJob(ctx, resource, "build-ui", job, false)
	if err != nil || outputs["image"] != priorResult.Outputs["image"] {
		t.Fatalf("matching result was not reused: %#v err=%v", outputs, err)
	}
	results, err := data.ListWorkflowJobResults(ctx, current.ID)
	if err != nil || len(results) != 1 || results[0].ReusedFromID != priorResult.ID {
		t.Fatalf("reuse evidence was not stored: %#v err=%v", results, err)
	}
}

func TestExecuteJobsRebuildsOnlyChangedSource(t *testing.T) {
	ctx := context.Background()
	data, config, resource := workflowRunnerFixture(t, "selective")
	now := time.Now().UTC()
	previousSources := map[string]core.WorkflowSourceRevision{
		"chart":   {Alias: "chart", Repository: "example/config", Branch: "main", CommitSHA: "chart-1"},
		"service": {Alias: "service", Repository: "example/service", Branch: "main", CommitSHA: "service-1"},
		"ui":      {Alias: "ui", Repository: "example/ui", Branch: "main", CommitSHA: "ui-1"},
	}
	currentSources := map[string]core.WorkflowSourceRevision{
		"chart":   previousSources["chart"],
		"service": {Alias: "service", Repository: "example/service", Branch: "main", CommitSHA: "service-2"},
		"ui":      previousSources["ui"],
	}
	previous := core.WorkflowRevision{ID: "selective-previous", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "succeeded", Trigger: "poll", Sources: previousSources, Outputs: map[string]map[string]string{}, CreatedAt: now}
	current := core.WorkflowRevision{ID: "selective-current", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "running", Trigger: "poll", Sources: currentSources, Outputs: map[string]map[string]string{}, CreatedAt: now.Add(time.Second)}
	if err := data.CreateWorkflowRevision(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateWorkflowRevision(ctx, current); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	chartPath := filepath.Join(root, "chart")
	servicePath, uiPath := filepath.Join(root, "service"), filepath.Join(root, "ui")
	for _, path := range []string{chartPath, servicePath, uiPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	serviceMarker := filepath.Join(root, "service-ran")
	jobs := map[string]JobSpec{
		"build-service": {RunFrom: "chart", Sources: []string{"service"}, Run: "printf service-2 > " + serviceMarker + "; printf 'imageTag=service-2\\n' > \"$DISPATCH_OUTPUT_FILE\"", Outputs: []string{"imageTag"}, Reuse: "onInputMatch"},
		"build-ui":      {RunFrom: "chart", Sources: []string{"ui"}, Run: "exit 91", Outputs: []string{"imageTag"}, Reuse: "onInputMatch"},
	}
	uiSources := map[string]core.WorkflowSourceRevision{"chart": previousSources["chart"], "ui": previousSources["ui"]}
	uiResult := core.WorkflowJobResult{ID: "selective-ui", ResourceID: resource.ID, RevisionID: previous.ID, JobName: "build-ui",
		Fingerprint: jobFingerprint("build-ui", jobs["build-ui"], uiSources, nil), State: "succeeded", Sources: uiSources,
		Outputs: map[string]string{"imageTag": "ui-1"}, CreatedAt: now}
	if err := data.CreateWorkflowJobResult(ctx, uiResult); err != nil {
		t.Fatal(err)
	}
	runtime := &jobRuntime{service: &Service{Store: data}, source: config, revision: current, root: root,
		paths: map[string]string{"chart": chartPath, "service": servicePath, "ui": uiPath}}
	outputs, err := runtime.executeJobs(ctx, resource, jobs, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if outputs["build-service"]["imageTag"] != "service-2" || outputs["build-ui"]["imageTag"] != "ui-1" {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	if _, err := os.Stat(serviceMarker); err != nil {
		t.Fatalf("changed service job did not run: %v", err)
	}
	results, err := data.ListWorkflowJobResults(ctx, current.ID)
	if err != nil || len(results) != 2 {
		t.Fatalf("unexpected job results: %#v err=%v", results, err)
	}
	for _, result := range results {
		if result.JobName == "build-ui" && result.ReusedFromID != uiResult.ID {
			t.Fatalf("unchanged UI job was not reused: %#v", result)
		}
		if result.JobName == "build-service" && result.ReusedFromID != "" {
			t.Fatalf("changed service job was reused: %#v", result)
		}
	}
}

func TestExecuteJobsRebuildsOnlyChangedUISource(t *testing.T) {
	ctx := context.Background()
	data, config, resource := workflowRunnerFixture(t, "selective-ui")
	now := time.Now().UTC()
	previousSources := map[string]core.WorkflowSourceRevision{
		"chart":   {Alias: "chart", Repository: "example/config", Branch: "main", CommitSHA: "chart-1"},
		"service": {Alias: "service", Repository: "example/service", Branch: "main", CommitSHA: "service-1"},
		"ui":      {Alias: "ui", Repository: "example/ui", Branch: "main", CommitSHA: "ui-1"},
	}
	currentSources := map[string]core.WorkflowSourceRevision{
		"chart":   previousSources["chart"],
		"service": previousSources["service"],
		"ui":      {Alias: "ui", Repository: "example/ui", Branch: "main", CommitSHA: "ui-2"},
	}
	previous := core.WorkflowRevision{ID: "selective-ui-previous", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "succeeded", Trigger: "poll", Sources: previousSources, Outputs: map[string]map[string]string{}, CreatedAt: now}
	current := core.WorkflowRevision{ID: "selective-ui-current", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "running", Trigger: "poll", Sources: currentSources, Outputs: map[string]map[string]string{}, CreatedAt: now.Add(time.Second)}
	if err := data.CreateWorkflowRevision(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateWorkflowRevision(ctx, current); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	chartPath := filepath.Join(root, "chart")
	servicePath, uiPath := filepath.Join(root, "service"), filepath.Join(root, "ui")
	for _, path := range []string{chartPath, servicePath, uiPath} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	uiMarker := filepath.Join(root, "ui-ran")
	jobs := map[string]JobSpec{
		"build-service": {RunFrom: "chart", Sources: []string{"service"}, Run: "exit 91", Outputs: []string{"imageTag"}, Reuse: "onInputMatch"},
		"build-ui":      {RunFrom: "chart", Sources: []string{"ui"}, Run: "printf ui-2 > " + uiMarker + "; printf 'imageTag=ui-2\\n' > \"$DISPATCH_OUTPUT_FILE\"", Outputs: []string{"imageTag"}, Reuse: "onInputMatch"},
	}
	serviceSources := map[string]core.WorkflowSourceRevision{"chart": previousSources["chart"], "service": previousSources["service"]}
	serviceResult := core.WorkflowJobResult{ID: "selective-service", ResourceID: resource.ID, RevisionID: previous.ID, JobName: "build-service",
		Fingerprint: jobFingerprint("build-service", jobs["build-service"], serviceSources, nil), State: "succeeded", Sources: serviceSources,
		Outputs: map[string]string{"imageTag": "service-1"}, CreatedAt: now}
	if err := data.CreateWorkflowJobResult(ctx, serviceResult); err != nil {
		t.Fatal(err)
	}
	runtime := &jobRuntime{service: &Service{Store: data}, source: config, revision: current, root: root,
		paths: map[string]string{"chart": chartPath, "service": servicePath, "ui": uiPath}}
	outputs, err := runtime.executeJobs(ctx, resource, jobs, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if outputs["build-service"]["imageTag"] != "service-1" || outputs["build-ui"]["imageTag"] != "ui-2" {
		t.Fatalf("unexpected outputs: %#v", outputs)
	}
	if _, err := os.Stat(uiMarker); err != nil {
		t.Fatalf("changed UI job did not run: %v", err)
	}
	results, err := data.ListWorkflowJobResults(ctx, current.ID)
	if err != nil || len(results) != 2 {
		t.Fatalf("unexpected job results: %#v err=%v", results, err)
	}
	for _, result := range results {
		if result.JobName == "build-service" && result.ReusedFromID != serviceResult.ID {
			t.Fatalf("unchanged service job was not reused: %#v", result)
		}
		if result.JobName == "build-ui" && result.ReusedFromID != "" {
			t.Fatalf("changed UI job was reused: %#v", result)
		}
	}
}

func TestExecuteJobRunsWhenPriorOutputIsMissing(t *testing.T) {
	ctx := context.Background()
	data, config, resource := workflowRunnerFixture(t, "missing-output")
	now := time.Now().UTC()
	sources := map[string]core.WorkflowSourceRevision{"ui": {Alias: "ui", Repository: "example/ui", Branch: "main", CommitSHA: "ui-1"}}
	previous := core.WorkflowRevision{ID: "missing-previous", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "succeeded", Trigger: "poll", Sources: sources, Outputs: map[string]map[string]string{}, CreatedAt: now}
	current := core.WorkflowRevision{ID: "missing-current", ResourceID: resource.ID, ConfigSHA: resource.ConfigSHA, SpecDigest: resource.SpecDigest,
		State: "running", Trigger: "poll", Sources: sources, Outputs: map[string]map[string]string{}, CreatedAt: now.Add(time.Second)}
	if err := data.CreateWorkflowRevision(ctx, previous); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateWorkflowRevision(ctx, current); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	uiPath := filepath.Join(root, "ui")
	if err := os.MkdirAll(uiPath, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "ui-ran")
	job := JobSpec{RunFrom: "ui", Run: "printf ran > " + marker + "; printf 'imageTag=ui-1\\n' > \"$DISPATCH_OUTPUT_FILE\"", Outputs: []string{"imageTag"}, Reuse: "onInputMatch"}
	priorResult := core.WorkflowJobResult{ID: "missing-prior-job", ResourceID: resource.ID, RevisionID: previous.ID, JobName: "build-ui",
		Fingerprint: jobFingerprint("build-ui", job, sources, nil), State: "succeeded", Sources: sources, Outputs: map[string]string{}, CreatedAt: now}
	if err := data.CreateWorkflowJobResult(ctx, priorResult); err != nil {
		t.Fatal(err)
	}
	runtime := &jobRuntime{service: &Service{Store: data}, source: config, revision: current, root: root, paths: map[string]string{"ui": uiPath}}
	outputs, err := runtime.executeJob(ctx, resource, "build-ui", job, false)
	if err != nil || outputs["imageTag"] != "ui-1" {
		t.Fatalf("missing output was not recovered: %#v err=%v", outputs, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("job with an incomplete prior result did not run: %v", err)
	}
}

func workflowRunnerFixture(t *testing.T, suffix string) (*store.SQLStore, core.ConfigSource, core.WorkflowResource) {
	t.Helper()
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), suffix+".db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: suffix + "-project", Name: "Project", CreatedAt: now}
	connection := core.GitHubAppConnection{ID: suffix + "-github", Name: "GitHub", WebURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3",
		AppID: 1, InstallationID: 2, WebhookURL: "https://dispatch.example/hook", EncryptedPrivateKey: "key", EncryptedWebhookSecret: "secret", State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	config := core.ConfigSource{ID: suffix + "-config", ProjectID: project.ID, GitHubAppID: connection.ID, Name: "Config", Repository: "example/config", Branch: "main", Path: "deployment",
		SyncMode: core.ConfigSyncPoll, PollIntervalSeconds: 60, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConfigSource(ctx, config); err != nil {
		t.Fatal(err)
	}
	resource := core.WorkflowResource{ID: suffix + "-resource", ConfigSourceID: config.ID, APIVersion: APIVersion, Kind: KindApplication, Name: suffix, Path: "deployment/app.yaml",
		Document: "test", SpecDigest: "sha256:spec", ConfigSHA: "config-sha", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateWorkflowResource(ctx, resource); err != nil {
		t.Fatal(err)
	}
	return data, config, resource
}

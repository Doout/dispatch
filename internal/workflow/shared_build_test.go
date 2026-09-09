package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestSlotsShareConcurrentBuildsByDeclaredInputs(t *testing.T) {
	ctx := context.Background()
	data, config, original := workflowRunnerFixture(t, "shared")
	service := &Service{Store: data}
	root := t.TempDir()
	marker := filepath.Join(root, "builds")
	job := JobSpec{RunFrom: "service", Reuse: "onInputMatch", Outputs: []string{"image"}, Run: fmt.Sprintf(`test -z "${DISPATCH_SOURCE_UI_COMMIT:-}"; printf 'build\n' >> %q; sleep 0.1; printf 'image=shared\n' > "$DISPATCH_OUTPUT_FILE"`, marker)}
	resources := []core.WorkflowResource{original}
	for i := 1; i < 3; i++ {
		item := original
		item.ID = fmt.Sprintf("slot-%d", i)
		item.Name = item.ID
		item.Path = item.ID + ".yaml"
		if err := data.CreateWorkflowResource(ctx, item); err != nil {
			t.Fatal(err)
		}
		resources = append(resources, item)
	}
	run := func(resource core.WorkflowResource, id, serviceSHA string) error {
		revision := core.WorkflowRevision{ID: id, ResourceID: resource.ID, State: "running", Sources: map[string]core.WorkflowSourceRevision{
			"service": {Alias: "service", Repository: "example/service", Branch: "main", CommitSHA: serviceSHA},
			"ui":      {Alias: "ui", Repository: "example/ui", Branch: "main", CommitSHA: id},
		}, CreatedAt: time.Now().UTC()}
		if err := data.CreateWorkflowRevision(ctx, revision); err != nil {
			return err
		}
		runtime := &jobRuntime{service: service, source: config, revision: revision, root: root, paths: map[string]string{"service": root}}
		outputs, err := runtime.executeJob(ctx, resource, "build-service", job, false)
		if err == nil && outputs["image"] != "shared" {
			return fmt.Errorf("missing shared output: %v", outputs)
		}
		return err
	}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for i, resource := range resources {
		wg.Add(1)
		go func(i int, resource core.WorkflowResource) {
			defer wg.Done()
			errs <- run(resource, fmt.Sprintf("run-%d", i), "service-1")
		}(i, resource)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		contents, err := os.ReadFile(marker)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Count(string(contents), "build\n")
	}
	if count() != 1 {
		t.Fatalf("identical concurrent builds ran %d times", count())
	}
	reused := 0
	for i := range resources {
		results, err := data.ListWorkflowJobResults(ctx, fmt.Sprintf("run-%d", i))
		if err != nil || len(results) != 1 {
			t.Fatalf("missing build evidence: %v %v", results, err)
		}
		if results[0].ReusedFromID != "" {
			reused++
		}
	}
	if reused != 2 {
		t.Fatalf("expected two reused results, got %d", reused)
	}
	if err := run(resources[1], "changed-service", "service-2"); err != nil {
		t.Fatal(err)
	}
	if count() != 2 {
		t.Fatal("changed service input was incorrectly reused")
	}
	failed, err := data.ListWorkflowJobResults(ctx, "changed-service")
	if err != nil || len(failed) != 1 {
		t.Fatalf("missing prior job: %v", err)
	}
	failed[0].State = "failed"
	if err := data.UpdateWorkflowJobResult(ctx, failed[0]); err != nil {
		t.Fatal(err)
	}
	if err := run(resources[2], "retry-failed", "service-2"); err != nil {
		t.Fatal(err)
	}
	if count() != 3 {
		t.Fatal("failed build was reused")
	}
	other := config
	other.ID = "other-config"
	other.Name = "Other configuration"
	other.Path = "other-deployment"
	if err := data.CreateConfigSource(ctx, other); err != nil {
		t.Fatal(err)
	}
	isolated := original
	isolated.ID = "isolated"
	isolated.ConfigSourceID = other.ID
	if err := data.CreateWorkflowResource(ctx, isolated); err != nil {
		t.Fatal(err)
	}
	if err := run(isolated, "isolated-run", "service-1"); err != nil {
		t.Fatal(err)
	}
	if count() != 4 {
		t.Fatal("build output crossed configuration boundaries")
	}
	if len(service.buildLocks) != 0 {
		t.Fatal("completed builds retained coordination locks")
	}
}

func TestSharedBuildWaitCanBeCancelled(t *testing.T) {
	service := &Service{}
	release, err := service.lockBuild(context.Background(), "same-inputs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.lockBuild(ctx, "same-inputs"); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait did not cancel: %v", err)
	}
	release()
	if len(service.buildLocks) != 0 {
		t.Fatal("cancelled waiter retained coordination lock")
	}
}

func TestSharedBuildFingerprintIncludesResolvedSecrets(t *testing.T) {
	job := JobSpec{RunFrom: "service", Run: "build", Reuse: "onInputMatch"}
	sources := map[string]core.WorkflowSourceRevision{"service": {Repository: "example/service", CommitSHA: "abc"}}
	initial := jobExecutionFingerprint("build", job, sources, nil, map[string]string{"TOKEN": "first"})
	rotated := jobExecutionFingerprint("build", job, sources, nil, map[string]string{"TOKEN": "second"})
	if initial == rotated {
		t.Fatal("rotated secret reused the old build identity")
	}
	if initial != jobExecutionFingerprint("build", job, sources, nil, map[string]string{"TOKEN": "first"}) {
		t.Fatal("identical secrets did not share a build identity")
	}
	if strings.Contains(initial, "first") {
		t.Fatal("secret appeared in stored build identity")
	}
}

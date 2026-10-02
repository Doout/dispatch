package store

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWorkflowCheckReceiptPersistence(t *testing.T) {
	testWorkflowCheckReceipt(t, filepath.Join(t.TempDir(), "checks.db"))
}
func TestWorkflowCheckReceiptPostgres(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL database required")
	}
	testWorkflowCheckReceipt(t, dsn)
}
func testWorkflowCheckReceipt(t *testing.T, dsn string) {
	ctx := context.Background()
	s := mutationStore(t, dsn)
	now := time.Now().UTC()
	suffix := ulid.Make().String()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	project, source, resource, revision := "project-"+suffix, "source-"+suffix, "resource-"+suffix, "run-"+suffix
	must(s.CreateProject(ctx, core.Project{ID: project, Name: project, CreatedAt: now}))
	must(s.CreateSecret(ctx, core.Secret{ID: "secret-" + suffix, Name: "workflow-check-fixture-" + suffix, EnvironmentVariable: "WORKFLOW_CHECK_" + suffix, Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture", CreatedAt: now}))
	must(s.CreateConfigSource(ctx, core.ConfigSource{ID: source, ProjectID: project, CredentialSecretID: "secret-" + suffix, Name: "Fixture", Repository: "example/repo", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateWorkflowResource(ctx, core.WorkflowResource{ID: resource, ConfigSourceID: source, Kind: "Application", Name: "Fixture", CreatedAt: now, UpdatedAt: now}))
	report := core.WorkflowCheckReport{ID: "report-" + suffix, RevisionID: revision, ResourceID: resource, ProjectID: project, GitHubAppID: "github", AppID: 42, APIURL: "https://api.github.com", ExternalID: "dispatch-check:" + suffix}
	run := core.WorkflowRevision{ID: revision, ResourceID: resource, State: "running", CreatedAt: now, Checks: []core.WorkflowCheckReport{report}}
	must(s.CreateWorkflowRevision(ctx, run))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var winner core.WorkflowCheckReport
	wins := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.ClaimWorkflowCheck(ctx, now, time.Minute)
			if errors.Is(err, ErrNotFound) {
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			wins++
			winner = r
			mu.Unlock()
		}()
	}
	wg.Wait()
	if wins != 1 || winner.AppID != 42 || winner.APIURL != "https://api.github.com" {
		t.Fatalf("lease/identity failure: wins=%d report=%+v", wins, winner)
	}
	must(s.BeginWorkflowCheckCreate(ctx, winner, now))
	restored, err := s.ClaimWorkflowCheck(ctx, now.Add(2*time.Minute), time.Minute)
	must(err)
	if restored.CreateState != "posting" || restored.LeaseToken == winner.LeaseToken {
		t.Fatalf("missing interrupted intent: %+v", restored)
	}
	restored.CheckID = 91
	restored.CreateState = "created"
	restored.State = "reported"
	restored.Status = "completed"
	restored.Conclusion = "cancelled"
	restored.Complete = true
	must(s.FinishWorkflowCheck(ctx, restored, now.Add(2*time.Minute)))
	if err := s.FinishWorkflowCheck(ctx, winner, now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old lease overwrote final receipt: %v", err)
	}
	run.State = "cancelled"
	must(s.UpdateWorkflowRevision(ctx, run))
	saved, err := s.ListWorkflowChecks(ctx, revision)
	must(err)
	if len(saved) != 1 || saved[0].CheckID != 91 || !saved[0].Complete {
		t.Fatalf("report lost: %+v", saved)
	}
	if _, err := s.ClaimWorkflowCheck(ctx, now.Add(time.Hour), time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finished receipt reclaimed: %v", err)
	}
}

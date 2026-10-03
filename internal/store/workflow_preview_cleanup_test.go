package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestPreviewCleanupFencesSQLite(t *testing.T) {
	testPreviewCleanupFences(t, filepath.Join(t.TempDir(), "preview.db"))
}
func TestPreviewCleanupFencesPostgres(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_PROVIDER_POSTGRES_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL not configured")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := "preview_" + strings.ToLower(ulid.Make().String())
	if _, err = admin.db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	defer admin.db.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`)
	u, _ := url.Parse(dsn)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	testPreviewCleanupFences(t, u.String())
}
func testPreviewCleanupFences(t *testing.T, dsn string) {
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	must(s.CreateProject(ctx, core.Project{ID: "project", Name: "project", CreatedAt: now}))
	must(s.CreatePrivateNetwork(ctx, core.PrivateNetwork{ID: "node", Name: "node", Driver: "dispatch_agent", Config: map[string]string{}, Details: map[string]string{}, State: "ready", CreatedAt: now, UpdatedAt: now}))
	must(s.RotateEdgeCredential(ctx, core.EdgeCredential{NetworkID: "node", EnrollmentHash: "hash", EnrollmentExpiresAt: now.Add(time.Hour), UpdatedAt: now}))
	must(s.EnrollEdgeCredential(ctx, "node", "hash", "public", "session", now, now.Add(time.Hour)))
	must(s.CreateServer(ctx, core.Server{ID: "server", Name: "server", Runtime: core.ServerRuntimeDocker, State: "ready", AgentNodeID: "node", AgentMode: "outbound-runtime", CreatedAt: now}))
	must(s.CreateSecret(ctx, core.Secret{ID: "credential", Name: "credential", Type: core.SecretTypeGitHubToken, EncryptedValue: "fixture", CreatedAt: now}))
	must(s.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", CredentialSecretID: "credential", Name: "source", CreatedAt: now, UpdatedAt: now}))
	resource := core.WorkflowResource{ID: "preview", ConfigSourceID: "source", Kind: "Application", Name: "preview", Temporary: true, Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}
	must(s.CreateWorkflowResource(ctx, resource))
	app := core.App{ID: "app", ProjectID: "project", ServerID: "server", Name: "app", BuildType: core.BuildTypeDockerfile, Generated: true, HelmProvenance: core.HelmProvenance{WorkflowResourceID: resource.ID}, State: "ready", CreatedAt: now}
	must(s.CreateApp(ctx, app))
	deployment := core.Deployment{ID: "deploy", AppID: app.ID, State: core.DeploymentStarting, CreatedAt: now}
	must(s.CreateDeployment(ctx, deployment))
	job := core.RuntimeJob{ID: "deploy-job", ServerID: "server", NodeID: "node", NodeGeneration: 1, ProjectID: "project", AppID: "app", DeploymentID: "deploy", Operation: "deploy", RequestDigest: "digest", EncryptedRequest: "private", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	must(s.CreateRuntimeJob(ctx, job))
	leased, err := s.LeaseRuntimeJob(ctx, "node", now, time.Minute)
	must(err)
	if leased == nil {
		t.Fatal("no initial lease")
	}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := s.BeginWorkflowPreviewCleanup(ctx, resource.ID, "removed", now.Add(time.Second))
			if e != nil {
				failures <- e
			} else {
				ids <- c.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	id := ""
	for value := range ids {
		if id != "" && id != value {
			t.Fatal("duplicate cleanup identities")
		}
		id = value
	}
	must(s.Close())
	s, err = Open(ctx, dsn)
	must(err)
	history, err := s.ListWorkflowPreviewCleanups(ctx, resource.ID)
	must(err)
	if len(history) != 1 || len(history[0].Apps) != 1 || history[0].Apps[0].AppID != app.ID {
		t.Fatalf("lost accepted inventory: %+v", history)
	}
	owned := history[0].Apps[0]
	if owned.NodeID != "node" || owned.NodeGeneration != 1 {
		t.Fatal("target enrollment was not pinned")
	}
	if err = s.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: "late", ResourceID: resource.ID, State: "queued", CreatedAt: now}); !errors.Is(err, ErrPreviewClosing) {
		t.Fatal("late revision accepted", err)
	}
	next := app
	next.ID = "late-app"
	if err = s.CreateApp(ctx, next); !errors.Is(err, ErrPreviewClosing) {
		t.Fatal("late app accepted", err)
	}
	next = app
	next.ServerID = "other"
	if err = s.UpdateApp(ctx, next); !errors.Is(err, ErrPreviewClosing) {
		t.Fatal("late app target change accepted", err)
	}
	deployment.ID = "late-deploy"
	if err = s.CreateDeployment(ctx, deployment); !errors.Is(err, ErrPreviewClosing) {
		t.Fatal("direct deploy accepted", err)
	}
	resource.Active = true
	resource.State = "ready"
	if err = s.UpdateWorkflowResource(ctx, resource); err == nil {
		t.Fatal("late resource update revived preview")
	}
	saved, err := s.GetRuntimeJob(ctx, job.ID)
	must(err)
	if saved.State != "unknown" || saved.EncryptedRequest != "" {
		t.Fatalf("execution uncertainty lost: %+v", saved)
	}
	if ok, e := s.RenewRuntimeJob(ctx, "node", job.ID, leased.LeaseToken, "late", "", now.Add(2*time.Second), time.Minute); !errors.Is(e, ErrRuntimeJobConflict) || ok {
		t.Fatal("stale lease renewed", ok, e)
	}
	if e := s.CompleteRuntimeJob(ctx, "node", job.ID, leased.LeaseToken, "succeeded", "result", now.Add(2*time.Second)); e == nil {
		t.Fatal("stale lease completed")
	}
	claim, err := s.ClaimWorkflowPreviewCleanup(ctx, id, time.Now().UTC())
	must(err)
	if claim == nil {
		t.Fatal("cleanup not claimable after restart")
	}
	other, err := s.ClaimWorkflowPreviewCleanup(ctx, id, time.Now().UTC())
	must(err)
	if other != nil {
		t.Fatal("cleanup lease stolen")
	}
	owned.State = "succeeded"
	must(s.SaveWorkflowPreviewCleanupApp(ctx, *claim, owned))
	if e := s.FinishWorkflowPreviewCleanup(ctx, *claim, "", time.Now().UTC()); e == nil {
		t.Fatal("cleanup completed without runtime/application proof")
	}
	inspect := job
	inspect.ID = "inspect"
	inspect.Operation = "inspect"
	inspect.CreatedAt = now.Add(3 * time.Second)
	must(s.CreateRuntimeJob(ctx, inspect))
	inspection, err := s.LeaseRuntimeJob(ctx, "node", now.Add(3*time.Second), time.Minute)
	must(err)
	if inspection == nil || inspection.ID != inspect.ID {
		t.Fatal("inspection blocked during cleanup", inspection)
	}
	must(s.CompleteRuntimeJob(ctx, "node", inspection.ID, inspection.LeaseToken, "succeeded", "evidence", now.Add(4*time.Second)))
	must(s.AcknowledgeRuntimeJob(ctx, app.ID, job.ID, inspect.ID, now.Add(2*time.Minute)))
	cleanupJob := job
	cleanupJob.ID = owned.JobID
	cleanupJob.Operation = "destroy"
	must(s.CreateRuntimeJob(ctx, cleanupJob))
	execution, err := s.LeaseRuntimeJob(ctx, "node", time.Now().UTC(), time.Minute)
	must(err)
	if execution == nil || execution.ID != cleanupJob.ID {
		t.Fatal("owned cleanup not leased", execution)
	}
	must(s.CompleteRuntimeJob(ctx, "node", execution.ID, execution.LeaseToken, "failed", "failure", time.Now().UTC()))
	retry, err := s.RetryWorkflowPreviewCleanupApp(ctx, *claim, owned)
	must(err)
	if retry.JobID == owned.JobID || len(retry.PreviousJobIDs) != 1 {
		t.Fatal("certain failure retry lost evidence", retry)
	}
	cleanupJob.ID = retry.JobID
	must(s.CreateRuntimeJob(ctx, cleanupJob))
	execution, err = s.LeaseRuntimeJob(ctx, "node", time.Now().UTC(), time.Minute)
	must(err)
	must(s.CompleteRuntimeJob(ctx, "node", execution.ID, execution.LeaseToken, "succeeded", "done", time.Now().UTC()))
	app.State = "closed"
	must(s.UpdateApp(ctx, app))
	retry.State = "succeeded"
	must(s.SaveWorkflowPreviewCleanupApp(ctx, *claim, retry))
	must(s.FinishWorkflowPreviewCleanup(ctx, *claim, "", time.Now().UTC()))
	history, err = s.ListWorkflowPreviewCleanups(ctx, resource.ID)
	must(err)
	if history[0].State != "succeeded" || len(history[0].Apps[0].PreviousJobIDs) != 1 {
		t.Fatal("terminal history missing", history)
	}
	if _, err = s.GetApp(ctx, app.ID); err != nil {
		t.Fatal("cleanup erased application history", err)
	}
	if err = s.DeleteConfigSource(ctx, "source"); !errors.Is(err, ErrPreviewHistory) {
		t.Fatal("configuration deletion erased preview evidence", err)
	}
	if _, err = s.GetServer(ctx, "server"); err != nil {
		t.Fatal("cleanup deleted shared server", err)
	}
	// Concurrent template creation is one binding and one generated resource.
	must(s.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "github", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateWorkflowPreviewTemplate(ctx, core.WorkflowPreviewTemplate{ID: "template", ConfigSourceID: "source", GitHubAppID: "github", Name: "template", Repository: "acme/app", Command: "/preview", CreatedAt: now, UpdatedAt: now}))
	var winner core.WorkflowPreviewTrigger
	var bindMu sync.Mutex
	success := 0
	errorsOut := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprint(i)
			trigger := core.WorkflowPreviewTrigger{ID: "trigger-" + id, TemplateID: "template", GitHubAppID: "github", Repository: "acme/app", PullRequestNumber: 42, Command: "/preview", LifetimeStartCommentID: "200", CreatedAt: now}
			generated := resource
			generated.ID = "generated-" + id
			generated.Name = "generated-" + id
			generated.Active = true
			generated.State = "ready"
			if err := s.CreateWorkflowResource(WithWorkflowPreviewTrigger(ctx, trigger), generated); err != nil {
				if !errors.Is(err, ErrPreviewBindingExists) {
					errorsOut <- err
				}
				return
			}
			bindMu.Lock()
			success++
			trigger.ResourceID = generated.ID
			winner = trigger
			bindMu.Unlock()
		}(i)
	}
	wg.Wait()
	close(errorsOut)
	for e := range errorsOut {
		t.Fatal(e)
	}
	if success != 1 {
		t.Fatalf("concurrent deliveries made %d previews", success)
	}
	must(s.CloseWorkflowPreviewTrigger(ctx, winner.ID, time.Now().UTC()))
	stale := winner
	stale.ID = "stale-trigger"
	stale.LifetimeStartCommentID = "199"
	generated := resource
	generated.ID = "stale-resource"
	generated.Name = "stale-resource"
	if err = s.CreateWorkflowResource(WithWorkflowPreviewTrigger(ctx, stale), generated); !errors.Is(err, ErrPreviewCommandRetired) {
		t.Fatal("old command recreated removed binding", err)
	}
	if _, err = s.GetWorkflowResource(ctx, generated.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("retired command orphaned a resource", err)
	}
	stale.ID = "new-trigger"
	stale.LifetimeStartCommentID = "201"
	generated.ID = "new-resource"
	generated.Name = "new-resource"
	must(s.CreateWorkflowResource(WithWorkflowPreviewTrigger(ctx, stale), generated))

}

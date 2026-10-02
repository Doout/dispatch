package store

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestEventActivityScopesDeduplicatesAndPreservesRunLinks(t *testing.T) {
	testEventActivityContract(t, filepath.Join(t.TempDir(), "events.db"))
}
func TestPostgresEventActivityWhenConfigured(t *testing.T) {
	dsn := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_TEST_POSTGRES_URL to a disposable database")
	}
	testEventActivityContract(t, dsn)
}
func testEventActivityContract(t *testing.T, dsn string) {
	ctx := context.Background()
	data, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, id := range []string{"visible", "hidden"} {
		if err := data.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	first := core.EventActivity{ID: ulid.Make().String(), ProjectID: "visible", RuleID: "template", Name: "Preview", Transport: "poll", Kind: "comment", State: "running", CreatedAt: now}
	if err := data.SaveEventActivity(ctx, "comment:1", first); err != nil {
		t.Fatal(err)
	}
	// Concurrent webhook and poll completions may arrive in either order.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			item := first
			item.ID = ulid.Make().String()
			item.Transport = "webhook"
			item.CreatedAt = now.Add(time.Minute)
			item.State = "processed"
			if i == 5 {
				item.RevisionIDs = []string{"run"}
				item.ResourceID = "workflow"
				item.PreviewURL = "https://preview.example.test"
			}
			if err := data.SaveEventActivity(ctx, "comment:1", item); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	items, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"visible"}})
	if err != nil || len(items) != 1 {
		t.Fatalf("duplicate delivery: %+v %v", items, err)
	}
	item := items[0]
	if item.ID != first.ID || item.Transport != "poll" || !item.CreatedAt.Equal(now) || len(item.RevisionIDs) != 1 || item.RevisionIDs[0] != "run" || item.ResourceID != "workflow" {
		t.Fatalf("lost delivery identity or run links: %+v", item)
	}
	hidden := first
	hidden.ID = ulid.Make().String()
	hidden.ProjectID = "hidden"
	if err := data.SaveEventActivity(ctx, "secret-event", hidden); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 55; i++ {
		item := first
		item.ID = ulid.Make().String()
		item.State = "processed"
		if err := data.SaveEventActivity(ctx, item.ID, item); err != nil {
			t.Fatal(err)
		}
	}
	// Existing scan rows must be filtered before the page limit is applied.
	for i := 0; i < 60; i++ {
		item := first
		item.ID = ulid.Make().String()
		item.Kind = []string{"branch_scan", "repository_check", "preview_check"}[i%3]
		item.State = "processed"
		if err := data.SaveEventActivity(ctx, item.ID, item); err != nil {
			t.Fatal(err)
		}
	}
	firstPage, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"visible"}, Transport: "poll", Limit: 50})
	if err != nil || len(firstPage) != 50 {
		t.Fatalf("first page: %d %v", len(firstPage), err)
	}
	secondPage, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"visible"}, Transport: "poll", Limit: 50, Before: firstPage[49].ID})
	if err != nil || len(secondPage) != 6 {
		t.Fatalf("second page: %d %v", len(secondPage), err)
	}
	for _, item := range append(firstPage, secondPage...) {
		if item.ProjectID != "visible" {
			t.Fatal("another project's activity leaked")
		}
	}
	count, err := data.CountEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"visible"}, Transport: "poll"})
	if err != nil || count != 56 {
		t.Fatalf("count: %d %v", count, err)
	}
	empty, err := data.SearchEventActivity(ctx, core.EventActivitySearch{})
	if err != nil || len(empty) != 0 {
		t.Fatal("empty project scope exposed events")
	}
	for _, state := range []string{"processed", "failed"} {
		item := first
		item.ID, item.Kind, item.State = ulid.Make().String(), "branch_scan", state
		if state == "processed" {
			item.RevisionIDs = []string{"accepted-run"}
		}
		if err := data.SaveEventActivity(ctx, item.ID, item); err != nil {
			t.Fatal(err)
		}
	}
	search := core.EventActivitySearch{ProjectIDs: []string{"visible"}, Transport: "poll", Limit: 2}
	items, err = data.SearchEventActivity(ctx, search)
	if err != nil || len(items) != 2 || items[0].State != "failed" || len(items[1].RevisionIDs) != 1 {
		t.Fatalf("meaningful scan activity missing: %+v %v", items, err)
	}
	count, err = data.CountEventActivity(ctx, search)
	if err != nil || count != 58 {
		t.Fatalf("meaningful scan count: %d %v", count, err)
	}
}

func TestPolledWorkflowActivityWaitsForRunOrFailure(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "workflow-events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(data.Migrate(ctx))
	now := time.Now().UTC()
	must(data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}))
	must(data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", CreatedAt: now, UpdatedAt: now}))
	must(data.CreateConfigSource(ctx, core.ConfigSource{ID: "source", ProjectID: "project", GitHubAppID: "github", Name: "Configuration", CreatedAt: now, UpdatedAt: now}))
	event := core.WorkflowEvent{ID: ulid.Make().String(), ConfigSourceID: "source", Provider: "poll", DeliveryID: "scan", Kind: "branch_scan", State: "running", CreatedAt: now}
	inserted, err := data.CreateWorkflowEvent(ctx, event)
	must(err)
	if !inserted {
		t.Fatal("scan reservation was not stored")
	}
	assertHistory := func(want int) []core.EventActivity {
		t.Helper()
		search := core.EventActivitySearch{ProjectIDs: []string{"project"}}
		items, err := data.SearchEventActivity(ctx, search)
		must(err)
		count, err := data.CountEventActivity(ctx, search)
		must(err)
		if len(items) != want || count != want {
			t.Fatalf("activity: %+v count=%d, want %d", items, count, want)
		}
		return items
	}
	assertHistory(0)
	event.State = "processed"
	must(data.UpdateWorkflowEvent(ctx, event))
	assertHistory(0)
	inserted, err = data.CreateWorkflowEvent(ctx, event)
	must(err)
	if inserted {
		t.Fatal("hiding a scan removed its delivery reservation")
	}

	event.ID, event.DeliveryID, event.State = ulid.Make().String(), "changed", "running"
	_, err = data.CreateWorkflowEvent(ctx, event)
	must(err)
	event.State, event.ResourceID, event.RevisionIDs = "processed", "app", []string{"run"}
	must(data.UpdateWorkflowEvent(ctx, event))
	items := assertHistory(1)
	if items[0].ResourceID != "app" || len(items[0].RevisionIDs) != 1 || items[0].RevisionIDs[0] != "run" {
		t.Fatalf("accepted run links missing: %+v", items)
	}

	event.ID, event.DeliveryID, event.State = ulid.Make().String(), "failure", "running"
	event.ResourceID, event.RevisionIDs = "", nil
	_, err = data.CreateWorkflowEvent(ctx, event)
	must(err)
	event.State, event.Error = "failed", "Unable to schedule deployment"
	must(data.UpdateWorkflowEvent(ctx, event))
	items = assertHistory(2)
	if items[0].State != "failed" || items[0].Message != event.Error {
		t.Fatalf("failed scan was hidden: %+v", items)
	}

	// Configuration changes can update templates without starting an application.
	event.ID, event.DeliveryID, event.Kind, event.State, event.Error = ulid.Make().String(), "config", "configuration_sync", "processed", ""
	_, err = data.CreateWorkflowEvent(ctx, event)
	must(err)
	assertHistory(3)
	// Webhook deliveries retain their own activity policy.
	event.ID, event.DeliveryID, event.Kind, event.Provider = ulid.Make().String(), "webhook", "branch_scan", "github"
	_, err = data.CreateWorkflowEvent(ctx, event)
	must(err)
	assertHistory(4)
}

func TestPollCheckJournalsFailuresAndRecoveryWithoutUnchangedNoise(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "checks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	item := core.EventActivity{ID: ulid.Make().String(), ProjectID: "project", RuleID: "check", Name: "Repository", Transport: "poll", Kind: "preview_check", State: "processed", Check: true, CreatedAt: now}
	for _, state := range []string{"processed", "processed", "failed", "failed", "processed", "processed"} {
		item.ID = ulid.Make().String()
		item.CreatedAt = item.CreatedAt.Add(time.Second)
		item.State = state
		item.Message = ""
		if state == "failed" {
			item.Message = "GitHub returned 503"
		}
		if err := data.SavePollCheck(ctx, "check", item); err != nil {
			t.Fatal(err)
		}
	}
	history, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}})
	if err != nil || len(history) != 2 || history[0].Kind != "poll_recovered" || history[1].Kind != "poll_failed" {
		t.Fatalf("failure transitions: %+v %v", history, err)
	}
	checks, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}, ChecksOnly: true})
	if err != nil || len(checks) != 1 || checks[0].State != "processed" || !checks[0].CreatedAt.Equal(item.CreatedAt) {
		t.Fatalf("latest check: %+v %v", checks, err)
	}
}

func TestEventActivityUpgradePreservesPreviewDeliveryIdentity(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "upgrade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	steps := migrationSteps(t, "sqlite")
	boundary := 0
	for i, step := range steps {
		if step.version == "063_event_activity" {
			boundary = i
			break
		}
	}
	if boundary == 0 {
		t.Fatal("event migration not found")
	}
	if err := data.applyMigrations(ctx, steps[:boundary]); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(data.CreateProject(ctx, core.Project{ID: "project", Name: "Project", CreatedAt: now}))
	must(data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", CreatedAt: now, UpdatedAt: now}))
	// Seed the historical schema directly. Current store writes include columns
	// added after this fixture's pre-063 migration boundary.
	_, err = data.db.ExecContext(ctx, `INSERT INTO config_sources(id,project_id,github_app_id,name,repository,branch,path,sync_mode,poll_interval_seconds,active,state,created_at,updated_at) VALUES('source','project','github','Source','team/ui','main','dispatch','poll',60,TRUE,'ready',?,?)`, stamp(now), stamp(now))
	must(err)
	must(data.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "workflow", ConfigSourceID: "source", Name: "Preview", Kind: "Application", Temporary: true, CreatedAt: now, UpdatedAt: now}))
	_, err = data.db.ExecContext(ctx, `INSERT INTO workflow_preview_templates(id,config_source_id,github_app_id,name,repository,command,preview_url,document,active,created_at,updated_at) VALUES('template','source','github','Template','team/ui','/preview','','',TRUE,?,?)`, stamp(now), stamp(now))
	must(err)
	for _, templateID := range []string{"", "template"} {
		number := 17
		if templateID != "" {
			number = 18
		}
		triggerID := "trigger" + templateID
		revisionID := "revision" + templateID
		_, err := data.db.ExecContext(ctx, `INSERT INTO workflow_preview_triggers(id,resource_id,github_app_id,repository,pull_request_number,command,template_id,created_at) VALUES(?,?,'github','team/ui',?,'/preview',?,?)`, triggerID, "workflow", number, nullString(templateID), stamp(now))
		must(err)
		_, err = data.db.ExecContext(ctx, `INSERT INTO workflow_revisions(id,resource_id,config_sha,spec_digest,state,trigger_name,sources,outputs,error,created_at) VALUES(?,?,'','','succeeded',?,'{}','{}','',?)`, revisionID, "workflow", "pull request comment "+triggerID, stamp(now))
		must(err)
		reserved, err := data.ReserveWorkflowPreviewComment(ctx, triggerID, triggerID)
		must(err)
		if !reserved {
			t.Fatal("comment reservation failed")
		}
		must(data.CompleteWorkflowPreviewComment(ctx, triggerID, triggerID, revisionID))
	}
	must(data.Migrate(ctx))
	source, err := data.GetConfigSource(ctx, "source")
	must(err)
	if source.ProjectID != "project" || source.GitHubAppID != "github" || source.Repository != "team/ui" || source.RepositoryID != 0 || source.RepositoryStatus != nil {
		t.Fatalf("upgrade changed the legacy source or repository identity defaults: %+v", source)
	}
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	must(err)
	if len(triggers) != 2 || len(triggers[0].SourceDefaults) != 0 || len(triggers[1].SourceDefaults) != 0 || triggers[0].TTL != "0" || triggers[0].ExpiresAt != nil || triggers[1].TTL != "0" || triggers[1].ExpiresAt != nil {
		t.Fatal("upgrade did not preserve legacy preview triggers", triggers)
	}
	for _, templateID := range []string{"", "template"} {
		triggerID := "trigger" + templateID
		ruleID := "preview:" + triggerID
		if templateID != "" {
			ruleID = "template:" + templateID
		}
		key := "delivery:github:team/ui:comment:github:team/ui:" + triggerID + ":" + ruleID
		must(data.SaveEventActivity(ctx, key, core.EventActivity{ID: ulid.Make().String(), ProjectID: "project", RuleID: ruleID, Transport: "poll", State: "running", CreatedAt: now}))
	}
	items, err := data.SearchEventActivity(ctx, core.EventActivitySearch{ProjectIDs: []string{"project"}})
	must(err)
	if len(items) != 2 {
		t.Fatalf("a retry duplicated imported comments: %+v", items)
	}
	for _, item := range items {
		if item.Transport != "history" || item.State != "processed" || len(item.RevisionIDs) != 1 || item.ResourceID != "workflow" {
			t.Fatalf("imported identity or run links changed: %+v", item)
		}
	}
}

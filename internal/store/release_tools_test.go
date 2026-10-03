package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestReleasePersistence(t *testing.T) {
	testReleasePersistence(t, filepath.Join(t.TempDir(), "release.db"))
}
func TestReleasePostgresPersistenceIntegration(t *testing.T) {
	dsn := isolatedPostgresURL(t, "DISPATCH_RELEASE_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_RELEASE_POSTGRES_URL to a disposable database")
	}
	testReleasePersistence(t, dsn)
}
func testReleasePersistence(t *testing.T, dsn string) {
	ctx := context.Background()
	data, err := Open(ctx, dsn)
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
	must(data.Migrate(ctx))
	now := time.Now().UTC()
	project := core.Project{ID: "release-project", Name: "release-project", CreatedAt: now}
	must(data.CreateProject(ctx, project))
	server := core.Server{ID: "release-server", Name: "release-server", Address: "local", Runtime: "docker", State: "ready", CreatedAt: now}
	must(data.CreateServer(ctx, server))
	app := core.App{ID: "release-app", ProjectID: project.ID, ServerID: server.ID, Name: "release-app", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	must(data.CreateApp(ctx, app))
	service := core.Service{ID: "release-service", ProjectID: project.ID, Name: "release-service", Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"url": {Sensitive: true, Configured: true, EncryptedValue: "original-encrypted-input"}}, CreatedAt: now, UpdatedAt: now}
	must(data.CreateService(ctx, service))
	must(data.ReplaceAppServiceBindings(ctx, app.ID, []core.ServiceBinding{{Alias: "db", ServiceRef: service.ID, Environment: map[string]string{"DATABASE_URL": "url"}}}))
	source := core.Deployment{ID: "release-source", AppID: app.ID, CommitSHA: "abc1234", State: core.DeploymentSucceeded, CreatedAt: now, Snapshot: core.DeploymentSnapshot{TargetID: server.ID, Values: map[string]any{"replicas": 2}}}
	must(data.CreateDeployment(ctx, source))
	must(data.SaveRuntimeArtifact(ctx, core.RuntimeArtifact{DeploymentID: source.ID, AppID: app.ID, ServerID: server.ID, ScopeID: source.ID, Ciphertext: "encrypted-original-runtime"}))
	source, err = data.GetDeployment(ctx, source.ID)
	must(err)
	service.Revision = 2
	service.Fields["url"] = core.ServiceField{Sensitive: true, Configured: true, EncryptedValue: "updated-encrypted-input"}
	must(data.UpdateService(ctx, service, 1))
	must(data.ReplaceAppServiceBindings(ctx, app.ID, nil))
	action := core.ReleaseAction{ID: "release-action", ProjectID: project.ID, AppID: app.ID, DeploymentID: "release-rollback", SourceDeploymentID: source.ID, Actor: "operator", Action: "deployment.rollback", CreatedAt: now}
	d := core.Deployment{ID: "release-rollback", AppID: app.ID, State: core.DeploymentQueued, CreatedAt: now.Add(time.Second)}
	must(data.CreateRollbackDeployment(ctx, d, source, action))
	artifact, err := data.GetRuntimeArtifact(ctx, d.ID)
	must(err)
	if artifact.ScopeID != source.ID || artifact.Ciphertext != "encrypted-original-runtime" || artifact.DeploymentID != d.ID {
		t.Fatal("rollback did not inherit immutable runtime inputs")
	}
	captured, err := data.GetDeploymentServiceBindings(ctx, d.ID)
	must(err)
	if len(captured) != 1 || captured[0].Service.Revision != 1 || captured[0].Service.Fields["url"].EncryptedValue != "original-encrypted-input" {
		t.Fatal("rollback captured edited credentials instead of retained inputs")
	}
	stored, err := data.GetDeployment(ctx, d.ID)
	must(err)
	if stored.CommitSHA != source.CommitSHA || stored.SpecDigest != source.SpecDigest || stored.Snapshot.Values["replicas"] != float64(2) {
		t.Fatal("rollback changed retained source inputs")
	}
	audit := core.AuditEvent{ID: "confirmed-release-delete", ActorID: "operator-id", ActorName: "Operator", ProjectID: project.ID, AppID: app.ID, ResourceID: app.ID, Action: "DELETE /api/v1/apps/{id}", ConfirmedAction: "delete", ConfirmedName: app.Name, ConfirmedVersion: "review-version", Outcome: "failed", CreatedAt: now}
	must(data.AppendAuditEvent(ctx, audit))
	events, err := data.ListAuditEvents(ctx, core.AuditFilter{ProjectIDs: []string{project.ID}, AppID: app.ID})
	must(err)
	if len(events) != 1 || events[0].ConfirmedName != app.Name || events[0].ConfirmedVersion != "review-version" || events[0].Outcome != "failed" {
		t.Fatal("confirmed audit metadata was not retained")
	}
	stale := d
	stale.ID = "stale-reviewed-rollback"
	stale.ExecutionAppName, stale.RollbackCurrentID = app.Name, source.ID
	stale.Acceptance = &core.DeploymentReview{ExpectedAppName: app.Name, ProjectID: project.ID, AppSpecDigest: "stale-spec"}
	if data.CreateRollbackDeployment(ctx, stale, source, action) == nil {
		t.Fatal("changed application accepted inside rollback transaction")
	}
	note := core.ReleaseNote{DeploymentID: d.ID, Notes: "Database schema is compatible", Links: []string{"https://example.test/repository/pull/1"}, Actor: "operator", UpdatedAt: now}
	action.ID = "release-note-action"
	action.Action = "release.notes"
	must(data.SaveReleaseNote(ctx, note, action))
	read, err := data.GetReleaseNote(ctx, d.ID)
	must(err)
	if read.Notes != note.Notes || len(read.Links) != 1 {
		t.Fatal("notes were not persisted")
	}
	rows, err := data.ListReleaseActions(ctx, project.ID, app.ID)
	must(err)
	if len(rows) != 2 {
		t.Fatalf("missing audit actions: %d", len(rows))
	}
	rows, err = data.ListReleaseActions(ctx, "other-project", app.ID)
	must(err)
	if len(rows) != 0 {
		t.Fatal("cross-project actions exposed")
	}
	invalid := d
	invalid.ID = "invalid-rollback"
	invalid.AppID = "another-app"
	if data.CreateRollbackDeployment(ctx, invalid, source, action) == nil {
		t.Fatal("accepted cross-application rollback")
	}
	duplicate := d
	duplicate.ID = "duplicate-audit-rollback"
	if data.CreateRollbackDeployment(ctx, duplicate, source, action) == nil {
		t.Fatal("accepted duplicate audit")
	}
	if _, err = data.GetDeployment(ctx, duplicate.ID); err != ErrNotFound {
		t.Fatal("failed audit transaction left an accepted deployment")
	}
}

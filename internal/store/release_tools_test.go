package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestReleasePersistence(t *testing.T) {
	testReleasePersistence(t, filepath.Join(t.TempDir(), "release.db"))
}
func TestReleasePostgresPersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_RELEASE_POSTGRES_URL")
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
	source, err = data.GetDeployment(ctx, source.ID)
	must(err)
	service.Revision = 2
	service.Fields["url"] = core.ServiceField{Sensitive: true, Configured: true, EncryptedValue: "updated-encrypted-input"}
	must(data.UpdateService(ctx, service, 1))
	must(data.ReplaceAppServiceBindings(ctx, app.ID, nil))
	action := core.ReleaseAction{ID: "release-action", ProjectID: project.ID, AppID: app.ID, DeploymentID: "release-rollback", SourceDeploymentID: source.ID, Actor: "operator", Action: "deployment.rollback", CreatedAt: now}
	d := core.Deployment{ID: "release-rollback", AppID: app.ID, State: core.DeploymentQueued, CreatedAt: now.Add(time.Second)}
	must(data.CreateRollbackDeployment(ctx, d, source, action))
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

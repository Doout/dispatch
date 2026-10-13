package workflow

import (
	"context"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/sourceerror"
	"github.com/doout/dispatch/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestSourceOutageKeepsLastValidConfiguration(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "outage.db"))
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
	must(data.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now}))
	must(data.CreateSecret(ctx, core.Secret{ID: "ssh", Name: "SSH", CreatedAt: now, UpdatedAt: now}))
	source := core.ConfigSource{CredentialSecretID: "ssh", ID: "config", ProjectID: "p", Name: "Config", Repository: "git@github.ibm.com:team/config.git", Branch: "main", Path: "deployment", State: "ready", SyncMode: core.ConfigSyncPoll, LastSeenSHA: "accepted", CreatedAt: now, UpdatedAt: now}
	must(data.CreateConfigSource(ctx, source))
	resource := core.WorkflowResource{ID: "app", ConfigSourceID: source.ID, Kind: KindApplication, Name: "App", Path: "deployment/app.yaml", Document: "accepted configuration", SpecDigest: "digest", State: "ready", Active: true, CreatedAt: now, UpdatedAt: now}
	must(data.CreateWorkflowResource(ctx, resource))
	service := &Service{Store: data}
	after, err := service.sourceError(ctx, source, &sourceerror.HTTPError{StatusCode: 503, Maintenance: true})
	if err == nil || after.State != "degraded" || after.LastSeenSHA != "accepted" || after.LastError != "GitHub is down for maintenance." {
		t.Fatalf("valid source invalidated: %+v %v", after, err)
	}
	kept, err := data.GetWorkflowResource(ctx, resource.ID)
	must(err)
	if kept.Document != resource.Document || kept.State != "ready" || !kept.Active {
		t.Fatal("outage changed the last valid resource")
	}
	bad := source
	bad.State = "invalid"
	bad.LastError = "configuration is missing spec"
	retained := sourceFailure(bad, &sourceerror.HTTPError{StatusCode: 503}, now)
	if retained.State != "invalid" || retained.LastError != bad.LastError {
		t.Fatal("outage hid a real configuration error")
	}
	rejected := sourceFailure(source, errors.New("branch is missing"), now)
	if rejected.State != "invalid" {
		t.Fatal("permanent source error treated as temporary")
	}
}

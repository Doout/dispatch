package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type changeBeforeCapture struct {
	store.Store
	change func() error
}

func (s changeBeforeCapture) CreateDeployment(ctx context.Context, d core.Deployment) error {
	if err := s.change(); err != nil {
		return err
	}
	return s.Store.CreateDeployment(ctx, d)
}
func TestReviewedAcceptanceRejectsEditsBeforeCapture(t *testing.T) {
	for _, kind := range []string{"app", "name", "project", "bindings", "service"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			data, err := store.Open(ctx, filepath.Join(t.TempDir(), "review.db"))
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
			for _, id := range []string{"project", "other"} {
				must(data.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}))
			}
			must(data.CreateServer(ctx, core.Server{ID: "server", Name: "server", Runtime: core.ServerRuntimeDocker, Address: "local", State: "ready", CreatedAt: now}))
			app := core.App{ID: "app", Name: "app", ProjectID: "project", ServerID: "server", BuildType: core.BuildTypeDockerfile, State: "ready", CreatedAt: now}
			must(data.CreateApp(ctx, app))
			service := core.Service{ID: "dependency", Name: "dependency", ProjectID: app.ProjectID, Type: "generic", Fields: map[string]core.ServiceField{"url": {Value: "https://original.example", Configured: true}}, Revision: 1, CreatedAt: now, UpdatedAt: now}
			must(data.CreateService(ctx, service))
			bindings := []core.ServiceBinding{{Alias: "dependency", ServiceRef: service.ID, Environment: map[string]string{"DEPENDENCY_URL": "url"}}}
			must(data.ReplaceAppServiceBindings(ctx, app.ID, bindings))
			review := core.DeploymentReview{ProjectID: app.ProjectID, AppSpecDigest: app.SpecDigest(), BindingsDigest: core.ServiceBindingConfigurationDigest(bindings), ServiceRevisions: map[string]int64{service.ID: 1}}
			wrapped := changeBeforeCapture{Store: data, change: func() error {
				switch kind {
				case "app":
					app.Domain = "new.example"
					return data.UpdateApp(ctx, app)
				case "name":
					app.Name = "renamed"
					return data.UpdateApp(ctx, app)
				case "project":
					app.ProjectID = "other"
					return data.UpdateApp(ctx, app)
				case "bindings":
					bindings[0].Environment = map[string]string{"OTHER_URL": "url"}
					return data.ReplaceAppServiceBindings(ctx, app.ID, bindings)
				default:
					service.Revision = 2
					service.Fields["url"] = core.ServiceField{Value: "https://updated.example", Configured: true}
					return data.UpdateService(ctx, service, 1)
				}
			}}
			runner := NewService(wrapped, SimulationExecutor{Delay: time.Millisecond})
			_, err = runner.StartReviewed(ctx, app.ID, "pinned-source", review)
			if !errors.Is(err, store.ErrDeploymentReviewChanged) {
				t.Fatalf("%s edit accepted: %v", kind, err)
			}
			deployments, err := data.ListDeployments(ctx, 10)
			must(err)
			if len(deployments) != 0 {
				t.Fatal("rejected review left a queued deployment")
			}
		})
	}
}
func TestOrdinaryAcceptanceRejectsConcurrentApplicationEdit(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "ordinary.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = data.SeedDemo(ctx); err != nil {
		t.Fatal(err)
	}
	apps, err := data.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var app core.App
	for _, candidate := range apps {
		active, e := data.ActiveDeploymentForApp(ctx, candidate.ID)
		if e == nil && active == nil {
			app = candidate
			break
		}
	}
	if app.ID == "" {
		t.Fatal("no idle fixture")
	}
	wrapper := changeBeforeCapture{Store: data, change: func() error { app.Domain = "changed.example"; return data.UpdateApp(ctx, app) }}
	runner := NewService(wrapper, SimulationExecutor{Delay: time.Millisecond})
	if _, err = runner.Start(ctx, app.ID, "sha"); !errors.Is(err, store.ErrDeploymentReviewChanged) {
		t.Fatal("stale runtime application accepted", err)
	}
}

package workflow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestServiceStageOverridesAndProjectIsolation(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "services.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"project", "foreign"} {
		if err = data.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []core.Service{{ID: "dev-id", Name: "dev-db", ProjectID: "project"}, {ID: "prod-id", Name: "prod-db", ProjectID: "project"}, {ID: "other-id", Name: "other-db", ProjectID: "foreign"}} {
		item.Type = "generic"
		item.Revision = 1
		item.Fields = map[string]core.ServiceField{"url": {Configured: true, Value: "endpoint"}}
		if err = data.CreateService(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Store: data}
	spec := DeploymentSpec{ServiceBindings: []core.ServiceBinding{{Alias: "database", ServiceRef: "dev-db", Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "url"}, SecretNameValues: []string{"database.existingSecret"}}}}}
	effective, err := s.effectiveServiceBindings(ctx, "project", spec, StageSpec{ServiceBindings: map[string]string{"database": "prod-db"}})
	if err != nil || effective[0].ServiceRef != "prod-id" {
		t.Fatalf("stage override failed: %#v %v", effective, err)
	}
	if spec.ServiceBindings[0].ServiceRef != "dev-db" {
		t.Fatal("stage override mutated the default")
	}
	if _, err = s.effectiveServiceBindings(ctx, "project", spec, StageSpec{ServiceBindings: map[string]string{"database": "other-id"}}); err == nil {
		t.Fatal("accepted another project's service")
	}
	spec.Helm.Bindings = map[string]OutputBinding{"database": {OutputRef: "build.result"}}
	if err = validateServiceDestinations(spec); err == nil {
		t.Fatal("accepted overlapping job output binding")
	}
}

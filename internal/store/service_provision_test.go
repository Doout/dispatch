package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestServiceProvisionRunRecovery(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := data.CreateProject(ctx, core.Project{ID: "project", Name: "project", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project", ServiceName: "database", State: "running", CreatedAt: now, StartedAt: &now}
	if err := data.CreateServiceProvisionRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	second := run
	second.ID = "second"
	if err := data.CreateServiceProvisionRun(ctx, second); err == nil {
		t.Fatal("concurrent provisioning of one service name must be rejected")
	}
	if err := data.RecoverInterruptedServiceProvisionRuns(ctx); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServiceProvisionRun(ctx, second); err != nil {
		t.Fatalf("failed runs must release the name: %v", err)
	}
	second.State = "failed"
	if err := data.UpdateServiceProvisionRun(ctx, second); err != nil {
		t.Fatal(err)
	}
	finished := core.ServiceProvisionRun{ID: "finished", TemplateID: "template", ProjectID: "project", ServiceName: "created", State: "running", CreatedAt: now}
	if err := data.CreateServiceProvisionRun(ctx, finished); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateService(ctx, core.Service{ID: "created-service", ProjectID: "project", Name: "created", Type: "generic", ProvisionRunID: finished.ID, Revision: 1, Fields: map[string]core.ServiceField{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.RecoverInterruptedServiceProvisionRuns(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err = data.GetServiceProvisionRun(ctx, finished.ID)
	if err != nil || finished.State != "succeeded" || finished.ServiceID != "created-service" {
		t.Fatalf("finished recovery: %+v, %v", finished, err)
	}
	got, err := data.GetServiceProvisionRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" || got.FinishedAt == nil || !strings.Contains(got.Error, "check the provider") {
		t.Fatalf("recovery: %+v", got)
	}
	if err := data.RecoverInterruptedServiceProvisionRuns(ctx); err != nil {
		t.Fatal(err)
	}
	runs, err := data.ListServiceProvisionRuns(ctx, "project")
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs: %+v, %v", runs, err)
	}
	states := map[string]string{}
	for _, item := range runs {
		states[item.ID] = item.State
	}
	if states["run"] != "failed" || states["second"] != "failed" || states["finished"] != "succeeded" {
		t.Fatalf("run states: %+v", states)
	}
}

package remoteruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestServiceResourceUnknownMutationRequiresInspectionAfterLease(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			b, base := brokerFixture(t)
			ctx := context.Background()
			data := b.Store.(*store.SQLStore)
			run := core.ServiceProvisionRun{ID: "service-run", TemplateID: "postgres", ProjectID: base.Application.ProjectID, ServiceName: "database", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: base.Server.ID}, State: "running", CreatedAt: time.Now().UTC()}
			if err := data.CreateServiceProvisionRun(ctx, run); err != nil {
				t.Fatal(err)
			}
			request := NewServiceRequest(core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Password: "private-frozen-password"}, core.DockerServiceProvision{ServerRef: base.Server.ID}, base.Server)
			job, err := b.Submit(ctx, "service-create", request)
			if err != nil {
				t.Fatal(err)
			}
			when := time.Now().UTC()
			if expired {
				when = when.Add(-2 * time.Minute)
			}
			lease, err := data.LeaseRuntimeJob(ctx, base.Server.AgentNodeID, when, LeaseDuration)
			if err != nil || lease == nil {
				t.Fatal(err)
			}
			if err = data.CompleteRuntimeJob(ctx, base.Server.AgentNodeID, job.ID, lease.LeaseToken, "unknown", "", when.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err = b.Submit(ctx, "unsafe-retry", request); !errors.Is(err, store.ErrRuntimeJobConflict) {
				t.Fatal("uncertain service mutation repeated", err)
			}
			inspect := request
			inspect.Operation = ServiceInspect
			inspected, err := b.Submit(ctx, "service-inspect", inspect)
			if err != nil {
				t.Fatal(err)
			}
			leased, err := b.Lease(ctx, base.Server.AgentNodeID)
			if err != nil || leased == nil {
				t.Fatal(err)
			}
			evidence := core.ServiceResourceInspection{RunID: run.ID, ProjectID: run.ProjectID, ServerID: base.Server.ID, Provider: "docker", State: "absent", StorageRetained: true}
			wrong := evidence
			wrong.ProjectID = "other"
			if err = b.Complete(ctx, base.Server.AgentNodeID, inspected.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "succeeded", ServiceResource: &wrong}}); err == nil {
				t.Fatal("foreign service evidence accepted")
			}
			if err = b.Complete(ctx, base.Server.AgentNodeID, inspected.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "succeeded", ServiceResource: &evidence}}); err != nil {
				t.Fatal(err)
			}
			err = data.ReconcileServiceRuntimeJobs(ctx, run.ID, inspected.ID, time.Now().Add(2*time.Minute))
			if expired && err != nil {
				t.Fatal("later inspection could not reconcile original service", err)
			}
			if !expired && !errors.Is(err, store.ErrRuntimeJobConflict) {
				t.Fatal("inspection taken during the old lease allowed a retry", err)
			}
			_, err = b.Submit(ctx, "reviewed-retry", request)
			if expired && err != nil {
				t.Fatal(err)
			}
			if !expired && !errors.Is(err, store.ErrRuntimeJobConflict) {
				t.Fatal("original operation lock lost", err)
			}
		})
	}
}

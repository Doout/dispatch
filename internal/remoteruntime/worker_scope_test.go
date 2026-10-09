package remoteruntime

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func setRuntimeWorkerScope(t *testing.T, broker *Broker, mode, project string) {
	t.Helper()
	data := broker.Store.(*store.SQLStore)
	node, err := data.GetPrivateNetwork(t.Context(), "node")
	if err != nil {
		t.Fatal(err)
	}
	node.Config = map[string]string{"workflowMode": mode, "workflowProjectId": project}
	if err = data.UpdatePrivateNetwork(t.Context(), node); err != nil {
		t.Fatal(err)
	}
}

func runtimeWorkerRequest(t *testing.T, broker *Broker, base Request, kind string) Request {
	t.Helper()
	if kind == "deployment" {
		return base
	}
	data := broker.Store.(*store.SQLStore)
	now := time.Now().UTC()
	run := core.ServiceProvisionRun{ID: "backup-source-run", TemplateID: "template", ProjectID: base.Application.ProjectID, ServiceName: "database", State: "queued", Target: &core.ServiceProvisionTarget{Provider: "docker", ServerID: base.Server.ID}, CreatedAt: now}
	resource := core.ServiceResource{RunID: run.ID, ProjectID: run.ProjectID, ServiceID: "database-service", Name: run.ServiceName, Target: *run.Target, State: "accepted", Policy: "retain", Revision: 1, OperationID: run.ID, EncryptedRequest: "encrypted-service-fixture", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateServiceResource(t.Context(), run, resource); err != nil {
		t.Fatal(err)
	}
	input := core.WorkloadBackupRequest{OperationID: "backup-operation", Action: "backup", Backup: core.WorkloadBackup{ID: "backup", ArtifactID: "backup", ProjectID: run.ProjectID, ServerID: base.Server.ID, NodeID: base.Server.AgentNodeID, SourceRunID: run.ID}, Source: core.ServiceProvisionRequest{Run: run, ServiceType: "postgresql", Password: "database-password"}, Storage: core.StorageResource{ProjectID: run.ProjectID, ServerID: base.Server.ID}, Key: strings.Repeat("a", 64)}
	return NewWorkloadBackupRequest(input, base.Server)
}

func TestRuntimeWorkerAdmissionRespectsProjectAndMode(t *testing.T) {
	for _, kind := range []string{"deployment", "backup"} {
		t.Run(kind, func(t *testing.T) {
			broker, base := brokerFixture(t)
			request := runtimeWorkerRequest(t, broker, base, kind)
			for index, test := range []struct {
				name, mode, project string
				allowed             bool
			}{
				{"legacy agent", "", "", true},
				{"tenant worker", "tenant", "", true},
				{"matching project", "tenant", "project", true},
				{"other project", "tenant", "other-project", false},
				{"managed worker", "managed", "", false},
				{"disabled worker", "disabled", "", false},
			} {
				setRuntimeWorkerScope(t, broker, test.mode, test.project)
				job, err := broker.Submit(t.Context(), fmt.Sprintf("operation-%d", index), request)
				if (err == nil) != test.allowed {
					t.Fatalf("%s: runtime admission allowed=%v, want %v: %v", test.name, err == nil, test.allowed, err)
				}
				if !test.allowed {
					continue
				}
				leased, err := broker.Lease(t.Context(), "node")
				if err != nil || leased == nil || leased.ID != job.ID {
					t.Fatal("lease allowed operation", err)
				}
				if err := broker.Complete(t.Context(), "node", job.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "failed"}}); err != nil {
					t.Fatal("finish allowed operation", err)
				}
			}
		})
	}
}

func TestRuntimeWorkerRechecksScopeBeforeLeasing(t *testing.T) {
	for _, kind := range []string{"deployment", "backup"} {
		for _, change := range []struct{ mode, project string }{{"tenant", "other-project"}, {"managed", ""}, {"disabled", ""}} {
			t.Run(kind+"/"+change.mode+change.project, func(t *testing.T) {
				broker, base := brokerFixture(t)
				request := runtimeWorkerRequest(t, broker, base, kind)
				setRuntimeWorkerScope(t, broker, "tenant", request.Application.ProjectID)
				if _, err := broker.Submit(t.Context(), "operation", request); err != nil {
					t.Fatal(err)
				}
				setRuntimeWorkerScope(t, broker, change.mode, change.project)
				if leased, err := broker.Lease(t.Context(), "node"); err == nil || leased != nil {
					t.Fatal("worker received credentials after its scope changed", err)
				}
			})
		}
	}
}

func TestRuntimeWorkerRechecksScopeOnRunningOperations(t *testing.T) {
	for _, kind := range []string{"deployment", "backup"} {
		t.Run(kind, func(t *testing.T) {
			broker, base := brokerFixture(t)
			request := runtimeWorkerRequest(t, broker, base, kind)
			setRuntimeWorkerScope(t, broker, "tenant", request.Application.ProjectID)
			job, err := broker.Submit(t.Context(), "operation", request)
			if err != nil {
				t.Fatal(err)
			}
			leased, err := broker.Lease(t.Context(), "node")
			if err != nil || leased == nil {
				t.Fatal("lease accepted operation", err)
			}
			for _, change := range []struct{ mode, project string }{{"tenant", "other-project"}, {"managed", ""}, {"disabled", ""}} {
				setRuntimeWorkerScope(t, broker, change.mode, change.project)
				if _, err := broker.Renew(t.Context(), "node", job.ID, Heartbeat{LeaseToken: leased.LeaseToken}); err == nil {
					t.Fatal("worker renewed an operation after its scope changed")
				}
				if err := broker.Complete(t.Context(), "node", job.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "failed"}}); err == nil {
					t.Fatal("worker completed an operation after its scope changed")
				}
			}
			setRuntimeWorkerScope(t, broker, "tenant", request.Application.ProjectID)
			if _, err := broker.Renew(t.Context(), "node", job.ID, Heartbeat{LeaseToken: leased.LeaseToken}); err != nil {
				t.Fatal("valid scope could not renew operation", err)
			}
			if err := broker.Complete(t.Context(), "node", job.ID, Completion{LeaseToken: leased.LeaseToken, Result: Result{State: "failed"}}); err != nil {
				t.Fatal("valid scope could not complete operation", err)
			}
		})
	}
}

func TestRuntimeWorkerStorageInspectionRequiresStoredTargetProject(t *testing.T) {
	broker, base := brokerFixture(t)
	data := broker.Store.(*store.SQLStore)
	setRuntimeWorkerScope(t, broker, "tenant", base.Application.ProjectID)
	request := NewStorageRequest(base.Server, nil)
	if _, err := broker.Submit(t.Context(), "unscoped-storage", request); err == nil {
		t.Fatal("restricted worker received tenant-wide storage inspection")
	}
	request.Server.ProjectID = base.Application.ProjectID
	if _, err := broker.Submit(t.Context(), "claimed-storage", request); err == nil {
		t.Fatal("request supplied its own target project authority")
	}
	server := base.Server
	server.AgentNodeID = ""
	if err := data.UpdateServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	server.ID, server.Name, server.AgentNodeID, server.ProjectID = "scoped-server", "Project target", base.Server.AgentNodeID, base.Application.ProjectID
	if err := data.CreateServer(t.Context(), server); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Submit(t.Context(), "scoped-storage", NewStorageRequest(server, nil)); err != nil {
		t.Fatal(err)
	}
	if leased, err := broker.Lease(t.Context(), "node"); err != nil || leased == nil {
		t.Fatal("worker could not inspect its project target", err)
	}
}

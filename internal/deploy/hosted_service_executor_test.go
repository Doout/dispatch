package deploy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

func TestHostedHelmServiceOperationsUseFreshReadsAndAcceptedDeletes(t *testing.T) {
	server := core.Server{ID: "target", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Namespace: "team", KubeconfigData: "explicit-kubeconfig"}}
	request := core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project"}, Password: "private-password", ServiceType: "postgresql"}
	spec := core.HelmServiceProvision{ServerRef: server.ID, Namespace: "team"}
	data := &hostedStoreFixture{server: server}
	e := &HostedExecutor{Store: data, AuthorizeService: func(_ context.Context, p workflowrunner.ServiceProvision, current core.Server) error {
		if current.ID != server.ID || p.Request.Password != request.Password {
			t.Fatal("service authorization lost accepted inputs")
		}
		return nil
	}}
	state := "absent"
	inspections := map[string]bool{}
	deletions := []string{}
	e.Runner = hostedRunnerFunc(func(ctx context.Context, r workflowrunner.Request, _ func(string)) (workflowrunner.Result, error) {
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var received workflowrunner.Request
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatal(err)
		}
		if err := e.AuthorizeRequest(ctx, received); err != nil {
			t.Fatal(err)
		}
		p := received.ServiceProvision
		if p.Kubeconfig != server.Kubernetes.KubeconfigData {
			t.Fatal("service operation lost stored credentials")
		}
		switch p.Action() {
		case "inspect":
			if inspections[r.RevisionID] {
				t.Fatal("service read reused a stale inspection identity")
			}
			inspections[r.RevisionID] = true
			return workflowrunner.Result{State: "succeeded", ServiceInspection: &core.ServiceResourceInspection{RunID: request.Run.ID, ProjectID: request.Run.ProjectID, ServerID: server.ID, Provider: "helm", State: state, ResourceID: "release-uid"}}, nil
		case "provision":
			if r.RevisionID != request.Run.ID {
				t.Fatal("provision identity changed")
			}
			state = "ready"
			return workflowrunner.Result{State: "succeeded", Outputs: map[string]string{"password": p.Request.Password}}, nil
		case "delete":
			if p.ExpectedResource != "release-uid" || p.OperationID != "accepted-delete" {
				t.Fatal("cleanup lost the reviewed resource identity")
			}
			deletions = append(deletions, r.RevisionID)
			state = "absent"
			return workflowrunner.Result{State: "succeeded"}, nil
		default:
			t.Fatal("unknown service operation")
		}
		return workflowrunner.Result{}, nil
	})
	ctx := context.Background()
	before, err := e.InspectServiceResource(ctx, request, spec, server)
	if err != nil || before.State != "absent" {
		t.Fatal("initial inspection", before, err)
	}
	outputs, err := e.Provision(ctx, request, spec, server)
	if err != nil || outputs["password"] != request.Password {
		t.Fatal("service provisioning", outputs, err)
	}
	after, err := e.InspectServiceResource(ctx, request, spec, server)
	if err != nil || after.State != "ready" {
		t.Fatal("post-create inspection reused absence", after, err)
	}
	for range 2 {
		if err := e.DeleteServiceResource(ctx, request, spec, server, "release-uid", "accepted-delete"); err != nil {
			t.Fatal(err)
		}
	}
	if len(deletions) != 2 || deletions[0] != deletions[1] {
		t.Fatal("cleanup retry changed its operation identity")
	}
	if err := e.DeleteServiceResource(ctx, request, spec, server, "release-uid", ""); err == nil {
		t.Fatal("cleanup without an acceptance succeeded")
	}
	e.Runner = hostedRunnerFunc(func(context.Context, workflowrunner.Request, func(string)) (workflowrunner.Result, error) {
		return workflowrunner.Result{State: "succeeded", ServiceInspection: &core.ServiceResourceInspection{RunID: "foreign", ProjectID: "project", ServerID: server.ID, Provider: "helm"}}, nil
	})
	if _, err := e.InspectServiceResource(ctx, request, spec, server); err == nil {
		t.Fatal("worker inspection for another service accepted")
	}
}

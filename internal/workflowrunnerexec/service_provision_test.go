package workflowrunnerexec

import (
	"context"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

const serviceKubeconfig = `apiVersion: v1
kind: Config
current-context: test
contexts:
- name: test
  context:
    cluster: test
    user: test
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:1
users:
- name: test
  user:
    token: explicit-worker-credential
`

func TestServiceProvisionWorkerUsesExplicitCredentials(t *testing.T) {
	r := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "template", RevisionID: "run", Mode: "tenant", ServiceProvision: &workflowrunner.ServiceProvision{
		Request: core.ServiceProvisionRequest{Password: "generated-password", Run: core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project"}},
		Spec:    core.HelmServiceProvision{ServerRef: "target"}, Server: core.Server{ID: "target", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Context: "test"}}, Kubeconfig: serviceKubeconfig,
	}}
	called := false
	result := executeServiceProvisionUsing(context.Background(), r, func(_ context.Context, request core.ServiceProvisionRequest, _ core.HelmServiceProvision, server core.Server) (map[string]string, error) {
		called = true
		if server.Kubernetes.KubeconfigPath != "" || server.Kubernetes.KubeconfigData != serviceKubeconfig {
			t.Fatal("worker did not use the explicit kubeconfig")
		}
		return map[string]string{"password": request.Password}, nil
	})
	if !called || result.State != "succeeded" || result.Outputs["password"] != "generated-password" || strings.Contains(result.Log, "generated-password") {
		t.Fatalf("provisioning credentials were lost or logged: %s", result.State)
	}
	r.ServiceProvision.Kubeconfig = strings.Replace(serviceKubeconfig, "token: explicit-worker-credential", "exec:\n      command: /controller/plugin", 1)
	result = executeServiceProvisionUsing(context.Background(), r, func(context.Context, core.ServiceProvisionRequest, core.HelmServiceProvision, core.Server) (map[string]string, error) {
		t.Fatal("exec credential reached the provisioner")
		return nil, nil
	})
	if result.State != "failed" {
		t.Fatal("host authentication plugin accepted")
	}
}

func TestServiceProvisionWorkerInspectsAndDeletesOwnedRelease(t *testing.T) {
	request := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "template", RevisionID: "inspection", Mode: "tenant", ServiceProvision: &workflowrunner.ServiceProvision{
		Operation: "inspect", Request: core.ServiceProvisionRequest{Run: core.ServiceProvisionRun{ID: "run", TemplateID: "template", ProjectID: "project"}},
		Spec: core.HelmServiceProvision{ServerRef: "target", Namespace: "team"}, Server: core.Server{ID: "target", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Context: "test", Namespace: "team"}}, Kubeconfig: serviceKubeconfig,
	}}
	inspected, deleted := false, false
	runtime := helmServiceOperations{inspect: func(_ context.Context, r core.ServiceProvisionRequest, spec core.HelmServiceProvision, server core.Server) (core.ServiceResourceInspection, error) {
		inspected = true
		if spec.Namespace != "team" || server.Kubernetes.KubeconfigData != serviceKubeconfig {
			t.Fatal("inspection did not use explicit worker credentials")
		}
		return core.ServiceResourceInspection{RunID: r.Run.ID, ProjectID: r.Run.ProjectID, ServerID: server.ID, Provider: "helm", State: "ready", ResourceID: "reviewed-release"}, nil
	}, remove: func(_ context.Context, _ core.ServiceProvisionRequest, _ core.HelmServiceProvision, server core.Server, expected string) error {
		deleted = true
		if expected != "reviewed-release" || server.Kubernetes.KubeconfigData != serviceKubeconfig {
			t.Fatal("cleanup did not retain reviewed ownership or credentials")
		}
		return nil
	}}
	result := executeServiceOperationUsing(context.Background(), request, runtime)
	if !inspected || result.State != "succeeded" || result.ServiceInspection == nil || result.ServiceInspection.ResourceID != "reviewed-release" {
		t.Fatal("inspection result missing", result)
	}
	request.ServiceProvision.Operation, request.ServiceProvision.OperationID, request.ServiceProvision.ExpectedResource = "delete", "accepted-cleanup", "reviewed-release"
	request.RevisionID = "accepted-cleanup"
	result = executeServiceOperationUsing(context.Background(), request, runtime)
	if !deleted || result.State != "succeeded" {
		t.Fatal("cleanup failed", result)
	}
	request.RevisionID = "different-operation"
	deleted = false
	result = executeServiceOperationUsing(context.Background(), request, runtime)
	if deleted || result.State != "failed" {
		t.Fatal("changed cleanup operation reached the runtime")
	}
}

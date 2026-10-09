package deploy

import (
	"context"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

type hostedStorageFixture struct {
	server   core.Server
	resource core.StorageResource
}

func (f *hostedStorageFixture) GetServer(context.Context, string) (core.Server, error) {
	return f.server, nil
}
func (f *hostedStorageFixture) GetStorage(context.Context, string) (core.StorageResource, error) {
	return f.resource, nil
}

func TestHostedStorageUsesWorkerAndRechecksTargetAndDeletionPolicy(t *testing.T) {
	server := core.Server{ID: "target", ProjectID: "project", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{KubeconfigData: "private-config", Namespace: "default"}}
	data := &hostedStorageFixture{server: server, resource: core.StorageResource{ID: "storage", ProjectID: "project", ServerID: server.ID, Kind: "kubernetes_pvc", Name: "data", Namespace: "preview", Identity: "uid", Evidence: "digest", Revision: 1, Ownership: "verified", Policy: "destroy", State: "present"}}
	var captured workflowrunner.Request
	backend := HostedStorageBackend{Store: data, Runner: hostedRunnerFunc(func(_ context.Context, r workflowrunner.Request, _ func(string)) (workflowrunner.Result, error) {
		captured = r
		return workflowrunner.Result{State: "succeeded", Storage: []core.StorageObservation{}}, nil
	})}
	scoped := server
	config := *server.Kubernetes
	config.Namespace = "preview"
	scoped.Kubernetes = &config
	if _, err := backend.Inspect(context.Background(), scoped); err != nil {
		t.Fatal(err)
	}
	if captured.StorageOperation == nil || captured.StorageOperation.Kubeconfig != "private-config" || captured.ProjectID != "project" {
		t.Fatal("worker lost target or credentials")
	}
	if err := backend.AuthorizeRequest(context.Background(), captured); err != nil {
		t.Fatal(err)
	}
	data.server.Kubernetes.Context = "changed"
	if backend.AuthorizeRequest(context.Background(), captured) == nil {
		t.Fatal("changed target accepted")
	}
	data.server.Kubernetes.Context = ""
	if err := backend.Delete(context.Background(), scoped, data.resource); err != nil {
		t.Fatal(err)
	}
	data.resource.Policy = "retain"
	if backend.AuthorizeRequest(context.Background(), captured) == nil {
		t.Fatal("retained storage remained deletable")
	}
	data.resource.Policy = "destroy"
	data.resource.Identity = "replacement"
	if backend.AuthorizeRequest(context.Background(), captured) == nil {
		t.Fatal("replacement storage remained deletable")
	}
}

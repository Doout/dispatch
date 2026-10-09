package workflowrunnerexec

import (
	"context"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/workflowrunner"
)

type storageFixture struct {
	called bool
	server core.Server
}

func (f *storageFixture) Inspect(_ context.Context, s core.Server) ([]core.StorageObservation, error) {
	f.called = true
	f.server = s
	return []core.StorageObservation{}, nil
}
func (f *storageFixture) Delete(context.Context, core.Server, core.StorageResource) error {
	f.called = true
	return nil
}
func TestHostedStorageWorkerRejectsPluginsAndRetainedDeletion(t *testing.T) {
	r := workflowrunner.Request{Version: workflowrunner.Version, ProjectID: "project", ResourceID: "target", RevisionID: "operation", Mode: "tenant", StorageOperation: &workflowrunner.StorageOperation{Server: core.Server{ID: "target", ProjectID: "project", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &core.KubernetesServerConfig{Context: "test", Namespace: "preview"}}, Kubeconfig: serviceKubeconfig}}
	fixture := &storageFixture{}
	result := executeStorage(context.Background(), r, fixture)
	if result.State != "succeeded" || result.Storage == nil || !fixture.called || fixture.server.Kubernetes.KubeconfigData != serviceKubeconfig {
		t.Fatal("worker did not inspect explicit target", result)
	}
	fixture.called = false
	r.StorageOperation.Resource = &core.StorageResource{ID: "pvc", ServerID: "target", Kind: "kubernetes_pvc", Ownership: "verified", Policy: "retain", State: "present"}
	if result = executeStorage(context.Background(), r, fixture); result.State != "failed" || fixture.called {
		t.Fatal("retained deletion reached backend", result)
	}
	r.StorageOperation.Resource = nil
	r.StorageOperation.Kubeconfig = strings.Replace(serviceKubeconfig, "token: explicit-worker-credential", "exec:\n      command: /controller/plugin", 1)
	if result = executeStorage(context.Background(), r, fixture); result.State != "failed" || fixture.called {
		t.Fatal("plugin reached storage backend", result)
	}
}

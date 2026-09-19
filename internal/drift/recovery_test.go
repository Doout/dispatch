package drift

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

const retainedPod = `apiVersion: v1
kind: Pod
metadata:
  name: recovered
spec:
  containers:
    - name: api
      image: example/api:v1
`

func legacyFixture(t *testing.T) (*Service, core.App, core.Deployment) {
	t.Helper()
	s, client, app, d := fixture(t, "")
	ctx := context.Background()
	now := d.CreatedAt.Add(time.Second)
	d.ID = "legacy"
	d.CreatedAt = now
	finished := now.Add(time.Minute)
	d.FinishedAt = &finished
	d.Snapshot = core.DeploymentSnapshot{TargetID: app.ServerID, Namespace: "default", Release: "drift", Values: map[string]any{}}
	if err := s.Store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	objects, err := Parse(retainedPod, "default")
	if err != nil {
		t.Fatal(err)
	}
	pod := objects[0]
	stampHelmOwnership(pod, "drift", "default")
	pod.SetUID("original-pod")
	pod.Object["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	if err = client.Tracker().Add(pod); err != nil {
		t.Fatal(err)
	}
	conn, _ := s.Connect(ctx, core.Server{})
	conn.Mapper.(*meta.DefaultRESTMapper).Add(schema.GroupVersionKind{Version: "v1", Kind: "Pod"}, meta.RESTScopeNamespace)
	s.ReadRelease = func(context.Context, core.Server, string, string, core.Deployment) (deploy.HelmDriftRelease, error) {
		return deploy.HelmDriftRelease{Manifest: retainedPod, Matched: true, Revision: 7}, nil
	}
	return s, app, d
}
func TestLegacyCheckRecoversExpectedManifestWithoutAdoptingLiveChanges(t *testing.T) {
	s, app, d := legacyFixture(t)
	ctx := context.Background()
	conn, _ := s.Connect(ctx, core.Server{})
	pods := conn.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).Namespace("default")
	if _, err := pods.Patch(ctx, "recovered", types.MergePatchType, []byte(`{"spec":{"containers":[{"name":"api","image":"example/api:edited"}]}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err := s.Check(ctx, app.ID)
	if err != nil || result.State != "out_of_sync" || result.Health != "healthy" || result.DeploymentID != d.ID {
		t.Fatal(result, err)
	}
	baseline, err := s.Store.GetDriftBaseline(ctx, d.ID)
	if err != nil || strings.Contains(baseline.Ciphertext, "example/api") {
		t.Fatal("baseline not encrypted", err)
	}
	if err = pods.Delete(ctx, "recovered", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = s.Check(ctx, app.ID)
	if err != nil || result.State != "out_of_sync" || result.Health != "degraded" {
		t.Fatal(result, err)
	}
}
func TestLegacyHealthDoesNotRequireMatchedBaseline(t *testing.T) {
	s, app, d := legacyFixture(t)
	s.ReadRelease = func(context.Context, core.Server, string, string, core.Deployment) (deploy.HelmDriftRelease, error) {
		return deploy.HelmDriftRelease{Manifest: retainedPod, Revision: 9}, nil
	}
	result, err := s.Check(context.Background(), app.ID)
	if err != nil || result.State != "unknown" || result.Health != "healthy" || result.CheckedAt == nil || result.DeploymentID != d.ID {
		t.Fatal(result, err)
	}
	if _, err = s.Store.GetDriftBaseline(context.Background(), d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("unmatched release was saved as desired baseline")
	}
}
func TestLegacyFailureKeepsDeploymentIdentityAndObservation(t *testing.T) {
	s, app, d := legacyFixture(t)
	s.ReadRelease = func(context.Context, core.Server, string, string, core.Deployment) (deploy.HelmDriftRelease, error) {
		return deploy.HelmDriftRelease{}, errors.New("Release history access denied.")
	}
	result, err := s.Check(context.Background(), app.ID)
	if err != nil || result.DeploymentID != d.ID || result.CheckedAt == nil || result.HealthMessage == "" {
		t.Fatal(result, err)
	}
	saved, err := s.Store.GetDriftCheck(context.Background(), app.ID)
	if err != nil || saved.Message != "Release history access denied." {
		t.Fatal(saved, err)
	}
}
func TestUnsupportedReadinessDoesNotHideKnownHealthyWorkloads(t *testing.T) {
	s, app, _ := legacyFixture(t)
	ctx := context.Background()
	conn, _ := s.Connect(ctx, core.Server{})
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "widget", "namespace": "default"}}}
	stampHelmOwnership(widget, "drift", "default")
	conn.Mapper.(*meta.DefaultRESTMapper).Add(schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Widget"}, meta.RESTScopeNamespace)
	if _, err := conn.Dynamic.Resource(schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}).Namespace("default").Create(ctx, widget, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	s.ReadRelease = func(context.Context, core.Server, string, string, core.Deployment) (deploy.HelmDriftRelease, error) {
		return deploy.HelmDriftRelease{Matched: true, Manifest: retainedPod + "---\napiVersion: example.test/v1\nkind: Widget\nmetadata:\n  name: widget\n", Revision: 7}, nil
	}
	result, err := s.Check(ctx, app.ID)
	if err != nil || result.Health != "healthy" || !strings.Contains(result.HealthMessage, "1 resources have no supported readiness check") {
		t.Fatal(result, err)
	}
}

func TestLegacyServiceBindingsRequireOriginalCredentialBaseline(t *testing.T) {
	s, app, d := legacyFixture(t)
	d.Snapshot.ServiceBindings = []core.AppliedServiceBinding{{}}
	if err := s.Store.UpdateDeploymentSnapshot(context.Background(), d.ID, d.Snapshot); err != nil {
		t.Fatal(err)
	}
	result, err := s.Check(context.Background(), app.ID)
	if err != nil || result.State != "unknown" || result.Health != "healthy" || !strings.Contains(result.Message, "credential baseline") {
		t.Fatal(result, err)
	}
	if _, err := s.Store.GetDriftBaseline(context.Background(), d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("adopted incomplete credential baseline", err)
	}
}

package drift

import (
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestManagedFieldComparisonPreservesDefaultsAndInjectedContainers(t *testing.T) {
	want := map[string]any{"spec": map[string]any{"replicas": int64(2), "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "api", "image": "example/api:v1", "env": []any{map[string]any{"name": "PASSWORD", "value": "saved-secret"}}}}}}}}
	live := map[string]any{"spec": map[string]any{"replicas": int64(3), "strategy": map[string]any{"type": "RollingUpdate"}, "template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "injected", "image": "sidecar:v1"}, map[string]any{"name": "api", "image": "example/api:v2", "imagePullPolicy": "Always", "env": []any{map[string]any{"name": "PASSWORD", "value": "new-secret"}, map[string]any{"name": "INJECTED", "value": "retain"}}}}}}}}
	diffs := []core.DriftDifference{}
	merged := Merge(want, live, "", &diffs).(map[string]any)
	if len(diffs) != 3 {
		t.Fatalf("expected three owned differences, got %#v", diffs)
	}
	encoded, _ := json.Marshal(diffs)
	if strings.Contains(string(encoded), "saved-secret") || strings.Contains(string(encoded), "new-secret") {
		t.Fatal("credential exposed")
	}
	if !strings.Contains(string(encoded), "example/api:v1") {
		t.Fatal("image change was not readable")
	}
	containers, _, _ := unstructured.NestedSlice(merged, "spec", "template", "spec", "containers")
	if len(containers) != 2 || containers[0].(map[string]any)["name"] != "injected" {
		t.Fatal("injected sidecar lost")
	}
	api := containers[1].(map[string]any)
	if api["imagePullPolicy"] != "Always" || len(api["env"].([]any)) != 2 {
		t.Fatal("unmanaged fields were removed")
	}
	diffs = nil
	Merge(want, merged, "", &diffs)
	if len(diffs) != 0 {
		t.Fatal("restored resource remains drifted", diffs)
	}
}
func TestHealthIsIndependentFromDrift(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"generation": int64(2)}, "spec": map[string]any{"replicas": int64(2)}, "status": map[string]any{"observedGeneration": int64(2), "readyReplicas": int64(1), "updatedReplicas": int64(2)}}}
	if health(object) != "progressing" {
		t.Fatal("unready deployment reported healthy")
	}
	_ = unstructured.SetNestedField(object.Object, int64(2), "status", "readyReplicas")
	if health(object) != "healthy" {
		t.Fatal("ready deployment was not healthy")
	}
	_ = unstructured.SetNestedField(object.Object, int64(1), "status", "observedGeneration")
	if health(object) != "progressing" {
		t.Fatal("stale status reported healthy")
	}
	object.SetKind("CustomThing")
	if health(object) != "unknown" {
		t.Fatal("unsupported health claimed healthy")
	}
}
func TestParseSecretAndOwnership(t *testing.T) {
	objects, err := Parse("apiVersion: v1\nkind: Secret\nmetadata: {name: database}\nstringData: {password: test-password}\n", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := objects[0].Object["stringData"]; ok {
		t.Fatal("stringData was not normalized")
	}
	if objects[0].Object["data"].(map[string]any)["password"] == "test-password" {
		t.Fatal("Secret normalization failed")
	}
	objects[0].SetUID("saved-uid")
	live := objects[0].DeepCopy()
	live.SetAnnotations(map[string]string{"meta.helm.sh/release-name": "unrelated"})
	if owned(objects[0], live, core.DriftBaseline{Release: "expected"}) {
		t.Fatal("accepted resource transferred to another release")
	}
}

func TestKubernetesDefaultsDoNotCausePermanentDrift(t *testing.T) {
	want := map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": float64(80), "targetPort": float64(8080)}}, "paused": false, "resources": map[string]any{"limits": map[string]any{"cpu": "1000m", "memory": "1024Mi"}}}}
	live := map[string]any{"spec": map[string]any{"ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080), "protocol": "TCP", "nodePort": int64(31000)}}, "resources": map[string]any{"limits": map[string]any{"cpu": "1", "memory": "1Gi"}}}}
	diffs := []core.DriftDifference{}
	Merge(want, live, "", &diffs)
	if len(diffs) > 0 {
		t.Fatal("server defaults or equivalent quantities cause drift", diffs)
	}
}

func TestSecretKeysNamedImageRemainRedacted(t *testing.T) {
	diff := visibleDifference("/data/image", "c2VjcmV0", "b3RoZXI=")
	if !diff.Redacted || diff.Expected != "[redacted]" || diff.Actual != "[redacted]" {
		t.Fatal("Secret image key leaked", diff)
	}
	diff = visibleDifference("/spec/password", int64(12345), int64(98765))
	if !diff.Redacted {
		t.Fatal("numeric credential leaked")
	}
}

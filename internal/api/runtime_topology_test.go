package api

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestManifestObjectRemovesRuntimeFields(t *testing.T) {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "checkout", "namespace": "dev", "resourceVersion": "12", "uid": "uid-1", "labels": map[string]any{"app": "checkout"}},
		"spec":     map[string]any{"replicas": int64(2)},
		"status":   map[string]any{"readyReplicas": int64(2)},
	}}

	manifest := manifestObject(object)
	if _, exists := manifest["status"]; exists {
		t.Fatal("status was included in the portable manifest")
	}
	metadata := manifest["metadata"].(map[string]any)
	if _, exists := metadata["resourceVersion"]; exists {
		t.Fatal("resourceVersion was included in the portable manifest")
	}
	if metadata["name"] != "checkout" || metadata["namespace"] != "dev" {
		t.Fatalf("identity fields were removed: %#v", metadata)
	}
}

func TestRepositoryLabelRemovesTransportAndHost(t *testing.T) {
	cases := map[string]string{
		"https://git.example.com/platform/deployments.git": "platform/deployments",
		"git@git.example.com:platform/deployments.git":     "platform/deployments",
		"platform/deployments":                             "platform/deployments",
	}
	for input, want := range cases {
		if got := repositoryLabel(input); got != want {
			t.Errorf("repositoryLabel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseReleaseManifestsSplitsAndCleansDocuments(t *testing.T) {
	items := parseReleaseManifests(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: checkout
  resourceVersion: "12"
spec:
  replicas: 2
status:
  readyReplicas: 2
---
apiVersion: v1
kind: Service
metadata:
  name: checkout
spec:
  type: ClusterIP
`)
	if len(items) != 2 {
		t.Fatalf("got %d manifests, want 2", len(items))
	}
	for _, item := range items {
		if item.Name != "checkout" || item.Document == "" {
			t.Fatalf("unexpected manifest: %#v", item)
		}
		if strings.Contains(item.Document, "status:") || strings.Contains(item.Document, "resourceVersion:") {
			t.Fatal("deployment document contains runtime status")
		}
	}
}

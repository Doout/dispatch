package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clienttesting "k8s.io/client-go/testing"
)

func TestHelmCleanupScopesStorageOwnerChainsToRelease(t *testing.T) {
	manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: preview49\n"
	for _, test := range []struct {
		name, deployment, ownerUID, annotation string
		missing, inaccessible, cyclic          bool
		blocked                                bool
	}{
		{name: "unrelated scratch volume", deployment: "connectivity"},
		{name: "unlabelled preview volume", deployment: "preview49", blocked: true},
		{name: "replaced owner pod", deployment: "preview49", ownerUID: "old-pod"},
		{name: "absent owner pod", deployment: "preview49", missing: true},
		{name: "retained deployment", deployment: "preview49", annotation: "keep"},
		{name: "uninspectable owner", deployment: "connectivity", inaccessible: true, blocked: true},
		{name: "cyclic owner chain", deployment: "connectivity", cyclic: true, blocked: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ownerUID := types.UID("pod-uid")
			if test.ownerUID != "" {
				ownerUID = types.UID(test.ownerUID)
			}
			objects := []runtime.Object{
				&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "scratch", Namespace: "shared", OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "worker", UID: ownerUID}}}},
				&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "replica", Namespace: "shared", UID: "replica-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: test.deployment, UID: "deployment-uid"}}}},
				&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: test.deployment, Namespace: "shared", UID: "deployment-uid"}},
			}
			if !test.missing {
				pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "shared", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "replica", UID: "replica-uid"}}}}
				if test.cyclic {
					pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "worker", UID: "pod-uid"}}
				}
				objects = append(objects, pod)
			}
			client := dynamicfake.NewSimpleDynamicClient(clientgoscheme.Scheme, objects...)
			if test.inaccessible {
				client.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, errors.New("forbidden")
				})
			}
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion, appsv1.SchemeGroupVersion})
			for _, kind := range []schema.GroupVersionKind{corev1.SchemeGroupVersion.WithKind("Pod"), appsv1.SchemeGroupVersion.WithKind("ReplicaSet"), appsv1.SchemeGroupVersion.WithKind("Deployment")} {
				mapper.Add(kind, meta.RESTScopeNamespace)
			}
			document := manifest
			if test.annotation != "" {
				document += "  annotations:\n    helm.sh/resource-policy: " + test.annotation + "\n"
			}
			err := checkHelmStorageOwnerReferences(context.Background(), client, mapper, "shared", document)
			if (err != nil) != test.blocked {
				t.Fatalf("blocked=%v, got %v", test.blocked, err)
			}
			for _, action := range client.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("storage inspection mutated runtime: %v", action)
				}
			}
		})
	}
}

func TestHelmCleanupFailsWhenStorageInventoryCannotBeRead(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(clientgoscheme.Scheme)
	client.PrependReactor("list", "persistentvolumeclaims", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("unavailable")
	})
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{corev1.SchemeGroupVersion})
	mapper.Add(corev1.SchemeGroupVersion.WithKind("Pod"), meta.RESTScopeNamespace)
	err := checkHelmStorageOwnerReferences(context.Background(), client, mapper, "shared", "apiVersion: v1\nkind: Pod\nmetadata:\n  name: preview\n")
	if err == nil || !strings.Contains(err.Error(), "cannot inspect storage owners") {
		t.Fatal("unavailable inventory accepted", err)
	}
}

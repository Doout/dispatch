package deploy

import (
	"context"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

// An unlabelled claim can still be deleted through a pod or controller owner.
// Follow those live owners only to objects this release will actually remove.
func checkHelmStorageOwnerReferences(ctx context.Context, client dynamic.Interface, mapper meta.RESTMapper, namespace, manifest string) error {
	targets := map[string]bool{}
	var capture func(map[string]any) error
	capture = func(document map[string]any) error {
		if document["kind"] == "List" {
			items, _ := document["items"].([]any)
			for _, value := range items {
				item, _ := value.(map[string]any)
				if err := capture(item); err != nil {
					return err
				}
			}
			return nil
		}
		if len(document) == 0 {
			return nil
		}
		object := &unstructured.Unstructured{Object: document}
		if object.GetAnnotations()["helm.sh/resource-policy"] == "keep" {
			return nil
		}
		kind := object.GroupVersionKind()
		mapping, err := mapper.RESTMapping(kind.GroupKind(), kind.Version)
		if err != nil || object.GetName() == "" {
			return errors.New("cannot inspect release objects before storage cleanup")
		}
		ns := ""
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			ns = object.GetNamespace()
			if ns == "" {
				ns = namespace
			}
		}
		targets[object.GroupVersionKind().GroupKind().String()+":"+ns+"/"+object.GetName()] = true
		return nil
	}
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if err == io.EOF {
			break
		}
		if err != nil {
			return errors.New("cannot inspect release objects before storage cleanup")
		}
		if err := capture(document); err != nil {
			return err
		}
	}
	if len(targets) == 0 {
		return nil
	}
	claims, err := client.Resource(schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return errors.New("cannot inspect storage owners before cleanup")
	}
	var inspect func(metav1.OwnerReference, string, map[string]bool) error
	inspect = func(owner metav1.OwnerReference, namespace string, seen map[string]bool) error {
		version, err := schema.ParseGroupVersion(owner.APIVersion)
		if err != nil {
			return errors.New("cannot inspect storage owner before cleanup")
		}
		mapping, err := mapper.RESTMapping(schema.GroupKind{Group: version.Group, Kind: owner.Kind}, version.Version)
		if err != nil {
			return errors.New("cannot inspect storage owner before cleanup")
		}
		ns := ""
		resource := client.Resource(mapping.Resource)
		var endpoint dynamic.ResourceInterface = resource
		if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
			ns = namespace
			endpoint = resource.Namespace(ns)
		}
		key := schema.GroupKind{Group: version.Group, Kind: owner.Kind}.String() + ":" + ns + "/" + owner.Name
		if seen[key] || len(seen) >= 32 {
			return errors.New("cannot resolve storage owner chain before cleanup")
		}
		object, err := endpoint.Get(ctx, owner.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return errors.New("cannot inspect storage owner before cleanup")
		}
		if owner.UID != "" && owner.UID != object.GetUID() {
			return nil
		}
		if targets[key] {
			return errors.New("storage has live garbage-collection owner references to this release; set its workload data retention to Retain and reconcile before cleanup")
		}
		seen[key] = true
		defer delete(seen, key)
		for _, parent := range object.GetOwnerReferences() {
			if err := inspect(parent, ns, seen); err != nil {
				return err
			}
		}
		return nil
	}
	for _, claim := range claims.Items {
		for _, owner := range claim.GetOwnerReferences() {
			if err := inspect(owner, claim.GetNamespace(), map[string]bool{}); err != nil {
				return err
			}
		}
	}
	return nil
}

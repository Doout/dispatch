package drift

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"io"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	"net/http"
	"time"
)

type Connection struct {
	Dynamic dynamic.Interface
	Mapper  meta.RESTMapper
	Close   func()
}
type Factory func(context.Context, core.Server) (Connection, error)

func Connect(ctx context.Context, server core.Server) (Connection, error) {
	if server.Kubernetes == nil {
		return Connection{}, errors.New("Kubernetes target required")
	}
	prepared, cleanup, err := kubeconfig.Prepare(*server.Kubernetes)
	if err != nil {
		return Connection{}, err
	}
	rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: prepared.KubeconfigPath}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: prepared.Context}).ClientConfig()
	if err != nil {
		cleanup()
		return Connection{}, err
	}
	cfg.Timeout = 10 * time.Second
	cfg.WrapTransport = func(next http.RoundTripper) http.RoundTripper { return contextualTransport{ctx: ctx, next: next} }
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		cleanup()
		return Connection{}, err
	}
	groups, err := restmapper.GetAPIGroupResources(dc)
	if err != nil {
		cleanup()
		return Connection{}, errors.New("resource discovery unavailable")
	}
	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		cleanup()
		return Connection{}, err
	}
	if err = ctx.Err(); err != nil {
		cleanup()
		return Connection{}, err
	}
	return Connection{Dynamic: client, Mapper: restmapper.NewDiscoveryRESTMapper(groups), Close: cleanup}, nil
}
func (c Connection) resource(obj *unstructured.Unstructured, namespace string) (dynamic.ResourceInterface, error) {
	mapping, err := c.Mapper.RESTMapping(obj.GroupVersionKind().GroupKind(), obj.GroupVersionKind().Version)
	if err != nil {
		return nil, err
	}
	if mapping.Scope.Name() == meta.RESTScopeNameNamespace {
		if obj.GetNamespace() == "" {
			obj.SetNamespace(namespace)
		}
		return c.Dynamic.Resource(mapping.Resource).Namespace(obj.GetNamespace()), nil
	}
	obj.SetNamespace("")
	return c.Dynamic.Resource(mapping.Resource), nil
}
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
func Parse(manifest, namespace string) ([]*unstructured.Unstructured, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewBufferString(manifest), 4096)
	out := []*unstructured.Unstructured{}
	seen := map[string]bool{}
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("cannot parse deployed resources")
		}
		if len(object) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{Object: object}
		if obj.GetKind() == "" || obj.GetAPIVersion() == "" || obj.GetName() == "" {
			return nil, errors.New("deployed resource has no stable identity")
		}
		if obj.GetKind() == "List" {
			return nil, errors.New("resource lists require individually named manifests")
		}
		if obj.GetAnnotations()["helm.sh/hook"] != "" {
			continue
		}
		if obj.GetNamespace() == "" {
			obj.SetNamespace(namespace)
		}
		if obj.GetKind() == "Secret" {
			data, _, _ := unstructured.NestedMap(obj.Object, "data")
			if data == nil {
				data = map[string]any{}
			}
			plain, _, _ := unstructured.NestedStringMap(obj.Object, "stringData")
			for k, v := range plain {
				data[k] = base64.StdEncoding.EncodeToString([]byte(v))
			}
			delete(obj.Object, "stringData")
			obj.Object["data"] = data
		}
		key := obj.GetAPIVersion() + "/" + obj.GetKind() + "/" + obj.GetNamespace() + "/" + obj.GetName()
		if seen[key] {
			return nil, errors.New("duplicate deployed resource")
		}
		seen[key] = true
		out = append(out, obj)
		if len(out) > 500 {
			return nil, errors.New("drift checks support at most 500 deployed resources")
		}
	}
	return out, nil
}

type contextualTransport struct {
	ctx  context.Context
	next http.RoundTripper
}

func (t contextualTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.next.RoundTrip(r.Clone(t.ctx))
}

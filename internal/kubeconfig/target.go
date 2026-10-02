package kubeconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/doout/dispatch/internal/core"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// InspectTarget uses only the selected kubeconfig context. It never falls back
// to the controller's default kubeconfig or in-cluster service account.
func InspectTarget(ctx context.Context, config core.KubernetesServerConfig) (core.KubernetesTargetEvidence, error) {
	var evidence core.KubernetesTargetEvidence
	prepared, cleanup, err := Prepare(config)
	if err != nil {
		return evidence, errors.New("Cannot prepare the selected Kubernetes credentials.")
	}
	defer cleanup()
	if prepared.KubeconfigPath == "" || prepared.Context == "" || prepared.Namespace == "" {
		return evidence, errors.New("Select a kubeconfig context and an existing namespace.")
	}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: prepared.KubeconfigPath}, &clientcmd.ConfigOverrides{CurrentContext: prepared.Context})
	connection, err := loader.ClientConfig()
	if err != nil {
		return evidence, errors.New("The selected kubeconfig context cannot be loaded.")
	}
	if connection.ExecProvider != nil || connection.AuthProvider != nil || connection.BearerTokenFile != "" {
		return evidence, errors.New("Use a kubeconfig with embedded credentials. Credential commands, token files and authentication plugins are not supported for registered targets.")
	}
	if connection.Insecure {
		return evidence, errors.New("Kubernetes targets require TLS certificate verification. Supply the cluster CA certificate.")
	}
	endpoint, err := url.Parse(connection.Host)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return evidence, errors.New("Use a Kubernetes HTTPS endpoint without embedded credentials or query parameters.")
	}
	connection.Timeout = 10 * time.Second
	connection.QPS, connection.Burst = 20, 40
	connection.ContentType, connection.AcceptContentTypes = "application/json", "application/json"
	client, err := kubernetes.NewForConfig(connection)
	if err != nil {
		return evidence, errors.New("Cannot initialize the selected Kubernetes connection.")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var info struct {
		GitVersion string `json:"gitVersion"`
	}
	raw, err := client.Discovery().RESTClient().Get().AbsPath("/version").Do(ctx).Raw()
	if err != nil || json.Unmarshal(raw, &info) != nil {
		return evidence, errors.New("Cannot read the Kubernetes version. Check the selected endpoint, CA and credentials.")
	}
	parsed, err := version.ParseSemantic(info.GitVersion)
	if err != nil || parsed.Major() != 1 || parsed.Minor() < 35 || parsed.Minor() > 36 {
		return evidence, errors.New("Unsupported Kubernetes version. Registration currently accepts Kubernetes 1.35 and 1.36, including K3s builds of those versions.")
	}
	for _, api := range []struct {
		path      string
		resources []string
	}{
		{"/api/v1", []string{"pods", "services", "secrets", "configmaps", "persistentvolumeclaims"}},
		{"/apis/apps/v1", []string{"deployments", "statefulsets", "replicasets"}},
		{"/apis/batch/v1", []string{"jobs"}},
		{"/apis/networking.k8s.io/v1", []string{"ingresses"}},
	} {
		raw, err := client.Discovery().RESTClient().Get().AbsPath(api.path).Do(ctx).Raw()
		var resources metav1.APIResourceList
		if err != nil || json.Unmarshal(raw, &resources) != nil {
			return evidence, fmt.Errorf("Required Kubernetes API %s is unavailable.", api.path)
		}
		for _, name := range api.resources {
			found := false
			for _, resource := range resources.APIResources {
				if resource.Name == name && resource.Namespaced {
					found = true
					break
				}
			}
			if !found {
				return evidence, fmt.Errorf("Required namespaced Kubernetes resource %s at %s is unavailable.", name, api.path)
			}
		}
	}
	cluster, err := client.CoreV1().Namespaces().Get(ctx, "kube-system", metav1.GetOptions{})
	if err != nil || cluster.UID == "" {
		return evidence, errors.New("Cannot verify cluster identity. Grant get access to the kube-system namespace.")
	}
	namespace, err := client.CoreV1().Namespaces().Get(ctx, prepared.Namespace, metav1.GetOptions{})
	if err != nil || namespace.UID == "" || namespace.DeletionTimestamp != nil {
		return evidence, errors.New("The selected namespace is unavailable. Create it and grant get access before registration.")
	}
	for _, required := range targetPermissions(prepared.Namespace) {
		review, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, &authorizationv1.SelfSubjectAccessReview{Spec: authorizationv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &required}}, metav1.CreateOptions{})
		if err != nil {
			return evidence, errors.New("Cannot check Kubernetes permissions. Allow selfsubjectaccessreviews and retry registration.")
		}
		if !review.Status.Allowed || review.Status.Denied || review.Status.EvaluationError != "" {
			return evidence, fmt.Errorf("The selected credentials need %s access to %s%s in namespace %s.", required.Verb, required.Resource, subresourceSuffix(required.Subresource), prepared.Namespace)
		}
	}
	return core.KubernetesTargetEvidence{ClusterUID: string(cluster.UID), NamespaceUID: string(namespace.UID), Version: info.GitVersion, CheckedAt: time.Now().UTC()}, nil
}

func subresourceSuffix(value string) string {
	if value == "" {
		return ""
	}
	return "/" + value
}

func targetPermissions(namespace string) []authorizationv1.ResourceAttributes {
	var result []authorizationv1.ResourceAttributes
	for _, group := range []struct {
		name      string
		resources []string
	}{
		{"", []string{"pods", "services", "secrets", "configmaps", "persistentvolumeclaims"}},
		{"apps", []string{"deployments", "statefulsets", "replicasets"}},
		{"batch", []string{"jobs"}},
		{"networking.k8s.io", []string{"ingresses"}},
	} {
		for _, resource := range group.resources {
			for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
				result = append(result, authorizationv1.ResourceAttributes{Namespace: namespace, Group: group.name, Resource: resource, Verb: verb})
			}
		}
	}
	return append(result, authorizationv1.ResourceAttributes{Namespace: namespace, Resource: "pods", Subresource: "log", Verb: "get"})
}

package deploy

import (
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/doout/dispatch/internal/core"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/engine"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
)

func TestHelmTemplateLookupCannotReadAnotherNamespace(t *testing.T) {
	var secretReads atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1":
			json.NewEncoder(w).Encode(metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "secrets", Kind: "Secret", Namespaced: true}}})
		case "/api/v1/namespaces/owned/secrets/database", "/api/v1/namespaces/another/secrets/database":
			secretReads.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Secret", "data": map[string]string{"password": "fixture-secret"}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config := &rest.Config{Host: server.URL, TLSClientConfig: rest.TLSClientConfig{CAData: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})}, WrapTransport: func(next http.RoundTripper) http.RoundTripper {
		return namespaceTransport{next: next, namespace: "owned"}
	}}
	for _, namespace := range []string{"owned", "another"} {
		before := secretReads.Load()
		ch := &chart.Chart{Metadata: &chart.Metadata{Name: "lookup", Version: "1.0.0"}, Templates: []*chart.File{{Name: "templates/value.yaml", Data: []byte(`value: {{ (lookup "v1" "Secret" "` + namespace + `" "database").data.password }}`)}}}
		_, err := engine.New(config).Render(ch, map[string]interface{}{})
		if namespace == "owned" {
			if err != nil || secretReads.Load() != before+1 {
				t.Fatal("owned lookup failed", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "registered namespace") || secretReads.Load() != before {
			t.Fatal("foreign lookup was not blocked before reading data", err)
		}
	}
}

func TestHelmSelectedCredentialsIgnoreControllerOverrides(t *testing.T) {
	t.Setenv("HELM_KUBEAPISERVER", "https://another.invalid")
	t.Setenv("HELM_KUBETOKEN", "another-token")
	t.Setenv("HELM_KUBEASUSER", "another-user")
	t.Setenv("HELM_KUBEASGROUPS", "system:masters")
	t.Setenv("HELM_KUBEINSECURE_SKIP_TLS_VERIFY", "true")
	path := filepath.Join(t.TempDir(), "config")
	config := `apiVersion: v1
kind: Config
current-context: selected
contexts:
- name: selected
  context: {cluster: selected, user: selected}
clusters:
- name: selected
  cluster: {server: https://selected.invalid}
users:
- name: selected
  user: {token: selected-token}
`
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	server := core.Server{Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: path, Context: "selected", Namespace: "owned"}}
	client, err := newSDKHelmClient(server, "owned", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.(*sdkHelmClient).configuration.RESTClientGetter.ToRESTConfig()
	if err != nil || connection.Host != "https://selected.invalid" || connection.BearerToken != "selected-token" || connection.Insecure || connection.Timeout != helmAPIRequestTimeout || connection.Impersonate.UserName != "" || len(connection.Impersonate.Groups) != 0 {
		t.Fatal("target used controller credentials or TLS overrides", err)
	}
}

func TestHelmNamespaceTransportRejectsClusterAccessAndTraversal(t *testing.T) {
	for _, value := range []string{"/api/v1/secrets", "/api/v1/namespaces/other/secrets", "/api/v1/namespaces/owned/../other/secrets", "/api/v1/nodes/node/proxy", "/apis/apps/v1/deployments", "/api/v1/namespaces/owned/pods/pod/proxy/internal"} {
		if allowedNamespaceRequest(httptest.NewRequest(http.MethodGet, value, nil), "owned") {
			t.Fatal("unsafe path allowed", value)
		}
	}
	for _, value := range []string{"/version", "/api/v1", "/apis/apps/v1", "/api/v1/namespaces/owned", "/api/v1/namespaces/owned/secrets", "/apis/apps/v1/namespaces/owned/deployments/app"} {
		if !allowedNamespaceRequest(httptest.NewRequest(http.MethodGet, value, nil), "owned") {
			t.Fatal("owned path blocked", value)
		}
	}
	if allowedNamespaceRequest(httptest.NewRequest(http.MethodDelete, "/api/v1/namespaces/owned", nil), "owned") {
		t.Fatal("namespace deletion allowed")
	}
}

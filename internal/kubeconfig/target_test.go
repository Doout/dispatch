package kubeconfig

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/doout/dispatch/internal/core"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestInspectTargetChecksVersionIdentityAPIsAndPermissions(t *testing.T) {
	for _, tc := range []struct{ name, version, missingAPI, denied, want string }{
		{name: "kubernetes135", version: "v1.35.7"},
		{name: "k3s136", version: "v1.36.2+k3s1"},
		{name: "old version", version: "v1.34.9", want: "Unsupported Kubernetes version"},
		{name: "unverified future version", version: "v1.37.0", want: "Unsupported Kubernetes version"},
		{name: "removed ingress API", version: "v1.36.2", missingAPI: "/apis/networking.k8s.io/v1", want: "Required Kubernetes API"},
		{name: "missing identity permission", version: "v1.36.2", denied: "identity", want: "verify cluster identity"},
		{name: "missing write permission", version: "v1.36.2", denied: "secrets", want: "create access to secrets"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer selected-credential" {
					t.Error("wrong target credentials")
					http.Error(w, "private rejected body", 401)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == tc.missingAPI {
					http.Error(w, "private rejected body", 404)
					return
				}
				switch r.URL.Path {
				case "/version":
					json.NewEncoder(w).Encode(map[string]string{"gitVersion": tc.version})
				case "/api/v1/namespaces/kube-system", "/api/v1/namespaces/apps":
					if tc.denied == "identity" {
						http.Error(w, "private rejected body", 403)
						return
					}
					json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]string{"name": strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/"), "uid": "identity-" + r.URL.Path}})
				case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
					var review authorizationv1.SelfSubjectAccessReview
					if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
						t.Error(err)
						return
					}
					attr := review.Spec.ResourceAttributes
					if attr == nil || attr.Namespace != "apps" {
						t.Error("permission escaped selected namespace")
						return
					}
					allowed := attr.Resource != tc.denied || attr.Verb != "create"
					json.NewEncoder(w).Encode(map[string]any{"apiVersion": "authorization.k8s.io/v1", "kind": "SelfSubjectAccessReview", "status": map[string]bool{"allowed": allowed}})
				default:
					groups := map[string][]string{"/api/v1": {"pods", "services", "secrets", "configmaps", "persistentvolumeclaims"}, "/apis/apps/v1": {"deployments", "statefulsets", "replicasets"}, "/apis/batch/v1": {"jobs"}, "/apis/networking.k8s.io/v1": {"ingresses"}}
					names, ok := groups[r.URL.Path]
					if !ok {
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
						return
					}
					resources := metav1.APIResourceList{}
					for _, name := range names {
						resources.APIResources = append(resources.APIResources, metav1.APIResource{Name: name, Namespaced: true})
					}
					json.NewEncoder(w).Encode(resources)
				}
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			config := core.KubernetesServerConfig{Context: "selected", Namespace: "apps", KubeconfigData: fmt.Sprintf(`apiVersion: v1
kind: Config
current-context: unrelated
clusters:
- name: selected
  cluster:
    server: %s
    certificate-authority-data: %s
- name: unrelated
  cluster:
    server: https://unrelated.invalid
contexts:
- name: selected
  context:
    cluster: selected
    user: selected
- name: unrelated
  context:
    cluster: unrelated
    user: unrelated
users:
- name: selected
  user:
    token: selected-credential
- name: unrelated
  user:
    token: unrelated-credential
`, server.URL, base64.StdEncoding.EncodeToString(ca))}
			evidence, err := InspectTarget(context.Background(), config)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("got %v, want %s", err, tc.want)
				}
				if strings.Contains(err.Error(), "credential\"") || strings.Contains(err.Error(), "private rejected") {
					t.Fatal("private response escaped")
				}
				return
			}
			if err != nil || evidence.Version != tc.version || evidence.ClusterUID == "" || evidence.NamespaceUID == "" || evidence.CheckedAt.IsZero() {
				t.Fatalf("evidence=%+v error=%v", evidence, err)
			}
			before := requests
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := InspectTarget(ctx, config); err == nil || requests != before {
				t.Fatal("cancelled check sent a request")
			}
		})
	}
}

func TestValidatedTargetFreezesOnlySelectedCredentials(t *testing.T) {
	contents := `apiVersion: v1
kind: Config
current-context: unused
contexts:
- name: selected
  context: {cluster: selected, user: selected}
- name: unused
  context: {cluster: unused, user: unused}
clusters:
- name: selected
  cluster: {server: https://selected.invalid}
- name: unused
  cluster: {server: https://unused.invalid, certificate-authority: /must-not-read}
users:
- name: selected
  user: {token: selected-secret}
- name: unused
  user: {token: unused-secret}
`
	for _, source := range []string{"mounted", "stored"} {
		t.Run(source, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			config := core.KubernetesServerConfig{Context: "selected", Namespace: "apps", KubeconfigPath: path, Validation: &core.KubernetesTargetEvidence{ClusterUID: "cluster"}}
			if source == "stored" {
				config.KubeconfigPath = ""
				config.KubeconfigData = contents
			}
			prepared, cleanup, err := Prepare(config)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if err := os.WriteFile(path, []byte("replaced credentials"), 0600); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(prepared.KubeconfigPath)
			if err != nil || !strings.Contains(string(raw), "selected-secret") || strings.Contains(string(raw), "unused") || strings.Contains(string(raw), "replaced credentials") {
				t.Fatal("selected credential snapshot was not isolated", err)
			}
			info, err := os.Stat(prepared.KubeconfigPath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("credentials file is not private", err)
			}
			cleanup()
			if _, err := os.Stat(prepared.KubeconfigPath); !os.IsNotExist(err) {
				t.Fatal("credential snapshot survived cleanup")
			}
		})
	}
}

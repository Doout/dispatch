package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/store"
)

func TestOverviewRequiresConfiguredToken(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.Code, response.Body.String())
	}
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if !overview.Demo || len(overview.Apps) != 3 {
		t.Fatalf("unexpected overview: demo=%v apps=%d", overview.Demo, len(overview.Apps))
	}
}

func TestHealthDoesNotRequireToken(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", response.Code)
	}
}

func TestApplicationTemplateIsStoredWithoutDirectDeployment(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID, "serverId": overview.Servers[0].ID, "name": "PR preview",
		"buildType": "compose", "composeContent": "services:\n  app:\n    image: example/app", "template": true,
	})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create application template: %d %s", response.Code, response.Body.String())
	}
	var template core.App
	if err := json.NewDecoder(response.Body).Decode(&template); err != nil {
		t.Fatal(err)
	}
	if !template.Template || template.State != "template" {
		t.Fatalf("unexpected application template: %#v", template)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+template.ID+"/deployments", bytes.NewBufferString(`{"commitSha":"inline"}`)))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Template cannot be deployed")) {
		t.Fatalf("expected direct template deployment conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestPreviewGroupCRUDAndApplicationDeletionConflict(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	kubeconfig := testKubeconfig(t, "preview", "preview")

	response := httptest.NewRecorder()
	serverBody, _ := json.Marshal(map[string]any{"name": "group-cluster", "runtime": "kubernetes", "kubernetes": map[string]any{"kubeconfigPath": kubeconfig, "context": "preview"}})
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create Kubernetes server: %d %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appIDs := []string{}
	for _, name := range []string{"service", "ui"} {
		body, _ := json.Marshal(map[string]any{"projectId": overview.Projects[0].ID, "serverId": server.ID, "name": "group-" + name,
			"sourceRepo": "https://example.test/org/" + name, "buildType": "helm", "helmChart": "oci://charts/" + name, "domain": name + "-{preview}.example.test"})
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s app: %d %s", name, response.Code, response.Body.String())
		}
		var app core.App
		if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
			t.Fatal(err)
		}
		appIDs = append(appIDs, app.ID)
	}

	groupBody := map[string]any{"name": "full-stack", "command": "/preview", "components": []map[string]any{
		{"appId": appIDs[0], "alias": "service", "repository": "org/service", "defaultBranch": "main", "entrypoint": false, "dependsOn": []string{}},
		{"appId": appIDs[1], "alias": "ui", "repository": "org/ui", "defaultBranch": "main", "entrypoint": true, "dependsOn": []string{"service"}, "bindings": []map[string]string{{"source": "service.url", "helmValuePath": "config.backendUrl"}}},
	}}
	encoded, _ := json.Marshal(groupBody)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/preview-groups", bytes.NewReader(encoded)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create preview group: %d %s", response.Code, response.Body.String())
	}
	var group core.PreviewGroup
	if err := json.NewDecoder(response.Body).Decode(&group); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/apps/"+appIDs[0], nil))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected grouped application deletion conflict, got %d: %s", response.Code, response.Body.String())
	}

	groupBody["name"] = "full-stack-preview"
	encoded, _ = json.Marshal(groupBody)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/preview-groups/"+group.ID, bytes.NewReader(encoded)))
	if response.Code != http.StatusOK {
		t.Fatalf("update preview group: %d %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/preview-groups/"+group.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete preview group: %d %s", response.Code, response.Body.String())
	}
}

func TestApplicationRequiresReadyServer(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	serverBody := bytes.NewBufferString(`{"name":"remote-01","address":"10.0.0.8","runtime":"docker"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", serverBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.State != "pending" || server.AgentMode != "ssh-bootstrap" {
		t.Fatalf("expected remote server to require enrollment, got state=%s agentMode=%s", server.State, server.AgentMode)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId":  overview.Projects[0].ID,
		"serverId":   server.ID,
		"name":       "pending-app",
		"sourceRepo": "https://example.test/pending.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected pending target to return 409, got %d: %s", response.Code, response.Body.String())
	}
}

func TestHelmApplicationUsesReadyKubernetesServer(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	kubeconfig := testKubeconfig(t, "staging", "staging")

	serverBody, err := json.Marshal(map[string]any{
		"name": "preview-cluster", "runtime": "kubernetes",
		"kubernetes": map[string]any{"kubeconfigPath": kubeconfig, "context": "staging", "namespace": "previews"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Kubernetes server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID, "serverId": server.ID, "name": "preview-api", "buildType": "helm",
		"helmChart": "service", "helmRepository": "https://charts.example.test", "helmVersion": "1.2.3",
		"helmValues": "image:\n  tag: pr-42", "helmNamespace": "preview-42", "helmRelease": "preview-api-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Helm application creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var app core.App
	if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	if app.BuildType != core.BuildTypeHelm || app.HelmChart != "service" || app.HelmNamespace != "preview-42" {
		t.Fatalf("unexpected Helm application: %#v", app)
	}
}

func TestApplicationAcceptsPastedComposeWithoutRepository(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID,
		"serverId":  overview.Servers[0].ID,
		"name":      "pasted-compose",
		"composeContent": `services:
  app:
    image: ghcr.io/example/app:latest`,
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected pasted Compose application to return 201, got %d: %s", response.Code, response.Body.String())
	}
	responseBody := response.Body.Bytes()
	var app core.App
	if err := json.Unmarshal(responseBody, &app); err != nil {
		t.Fatal(err)
	}
	if app.SourceRepo != "" || app.BuildType != core.BuildTypeCompose || app.ComposePath != "compose.yml" {
		t.Fatalf("unexpected pasted Compose application: %#v", app)
	}
	if bytes.Contains(responseBody, []byte("ghcr.io/example/app")) {
		t.Fatal("stored Compose content must not be returned by the API")
	}
}

func TestLocalControllerTargetIsManagedAutomatically(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	body := bytes.NewBufferString(`{"name":"local-docker","address":"LOCAL","runtime":"docker","agentMode":"ssh-bootstrap"}`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", body))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("managed automatically")) {
		t.Fatalf("expected manual local creation to return a managed-target conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestManagedLocalControllerCannotBeChangedOrDeleted(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	server := overview.Servers[0]
	if server.Address != "local" {
		t.Fatalf("expected demo controller target, got %#v", server)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewBufferString(`{"name":"renamed","address":"local"}`)))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Managed server cannot be changed")) {
		t.Fatalf("expected managed local update to return 409, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/servers/"+server.ID, nil))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Managed server cannot be deleted")) {
		t.Fatalf("expected managed local deletion to return 409, got %d: %s", response.Code, response.Body.String())
	}
}

func TestKubernetesServerCanBeConfiguredWithoutAnApplication(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	previewConfig := testKubeconfig(t, "preview", "preview")
	eastConfig := testKubeconfig(t, "east", "east")

	body, err := json.Marshal(map[string]any{
		"name": "Preview cluster", "runtime": "k8s",
		"kubernetes": map[string]any{"kubeconfigPath": " " + previewConfig + " ", "context": " preview ", "namespace": " pull-requests "},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Kubernetes server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Runtime != core.ServerRuntimeKubernetes || server.State != "ready" || server.AgentMode != "direct" {
		t.Fatalf("unexpected Kubernetes server state: %#v", server)
	}
	if server.Address != previewConfig || server.Kubernetes == nil || server.Kubernetes.Context != "preview" || server.Kubernetes.Namespace != "pull-requests" {
		t.Fatalf("unexpected Kubernetes server configuration: %#v", server)
	}

	update, err := json.Marshal(map[string]any{
		"name":       "Preview cluster east",
		"kubernetes": map[string]any{"kubeconfigPath": eastConfig, "context": "east"},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewReader(update)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected Kubernetes server update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Address != eastConfig || server.Kubernetes == nil || server.Kubernetes.Namespace != "default" {
		t.Fatalf("unexpected updated Kubernetes server: %#v", server)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("expected overview to return 200, got %d", response.Code)
	}
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.Apps) != 0 || len(overview.Servers) != 1 {
		t.Fatalf("Kubernetes server should not require an application: servers=%d apps=%d", len(overview.Servers), len(overview.Apps))
	}
}

func TestKubernetesServerRequiresMountedKubeconfigPath(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewBufferString(`{"name":"Cluster","runtime":"kubernetes","kubernetes":{}}`)))
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("kubeconfig path")) {
		t.Fatalf("expected missing kubeconfig path to return 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestOpenShiftServerRequiresSafeNonInteractiveOCLogin(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	for _, body := range []string{
		`{"name":"OpenShift","runtime":"openshift","kubernetes":{}}`,
		`{"name":"OpenShift","runtime":"openshift","kubernetes":{"loginCommand":"kubectl get pods"}}`,
		`{"name":"OpenShift","runtime":"openshift","kubernetes":{"loginCommand":"oc login https://api.example.test"}}`,
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewBufferString(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("expected unsafe or interactive OpenShift login to return 400, got %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestOpenShiftRepairRejectsOtherServerTypes(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers/"+overview.Servers[0].ID+"/repair", bytes.NewBufferString(`{"loginCommand":"oc login --token=x"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected non-OpenShift repair conflict, got %d: %s", response.Code, response.Body.String())
	}
}

func TestKubernetesServerStoresPastedKubeconfigAndCAWithoutReturningSecrets(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer tlsServer.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}))
	kubeconfig := `apiVersion: v1
kind: Config
current-context: preview
contexts:
  - name: preview
    context: {cluster: preview-cluster, user: preview-user}
clusters:
  - name: preview-cluster
    cluster:
      server: https://cluster.example.test
      certificate-authority: /local/ca.crt
users:
  - name: preview-user
    user: {token: private-cluster-token}
`
	body, err := json.Marshal(map[string]any{"name": "Stored cluster", "runtime": "kubernetes", "kubernetes": map[string]any{
		"source": "stored", "kubeconfig": kubeconfig, "certificateAuthority": ca, "namespace": "previews",
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(body)))
	if response.Code != http.StatusCreated {
		t.Fatalf("store Kubernetes server: %d %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private-cluster-token")) || bytes.Contains(response.Body.Bytes(), []byte("BEGIN CERTIFICATE")) {
		t.Fatalf("server response exposed stored credentials: %s", response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Address != "stored" || server.Kubernetes == nil || !server.Kubernetes.KubeconfigStored || !server.Kubernetes.CertificateAuthorityStored || server.Kubernetes.KubeconfigPath != "" || server.Kubernetes.Context != "preview" {
		t.Fatalf("unexpected stored Kubernetes server: %#v", server)
	}
	update, _ := json.Marshal(map[string]any{"name": "Stored cluster renamed", "kubernetes": map[string]any{
		"source": "stored", "context": "preview", "namespace": "previews",
	}})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewReader(update)))
	if response.Code != http.StatusOK {
		t.Fatalf("update stored Kubernetes server: %d %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if !server.Kubernetes.KubeconfigStored || !server.Kubernetes.CertificateAuthorityStored {
		t.Fatalf("blank edit did not preserve stored credentials: %#v", server.Kubernetes)
	}
}

func TestApplicationRuntimeMustMatchServer(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()
	kubeconfig := testKubeconfig(t, "preview", "preview")
	serverBody, err := json.Marshal(map[string]any{
		"name": "preview-cluster", "runtime": "kubernetes",
		"kubernetes": map[string]any{"kubeconfigPath": kubeconfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected Kubernetes server creation, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId": overview.Projects[0].ID, "serverId": server.ID, "name": "wrong-runtime",
		"composeContent": "services:\n  app:\n    image: example/app",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("Docker server")) {
		t.Fatalf("expected runtime mismatch to return 400, got %d: %s", response.Code, response.Body.String())
	}
}

func TestUndeployedApplicationCanBeDeletedBeforeServerAndProject(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	kubeconfig := testKubeconfig(t, "preview", "preview")

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"name":"Preview"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected project creation, got %d: %s", response.Code, response.Body.String())
	}
	var project core.Project
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	serverBody, err := json.Marshal(map[string]any{
		"name": "preview-cluster", "runtime": "kubernetes",
		"kubernetes": map[string]any{"kubeconfigPath": kubeconfig},
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewReader(serverBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	appBody, err := json.Marshal(map[string]any{
		"projectId": project.ID, "serverId": server.ID, "name": "preview", "buildType": "helm",
		"helmChart": "oci://registry.example.test/charts/preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps", bytes.NewReader(appBody)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected app creation, got %d: %s", response.Code, response.Body.String())
	}
	var app core.App
	if err := json.NewDecoder(response.Body).Decode(&app); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/v1/apps/" + app.ID, "/api/v1/servers/" + server.ID, "/api/v1/projects/" + project.ID} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodDelete, target, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("expected %s deletion to return 204, got %d: %s", target, response.Code, response.Body.String())
		}
	}
}

func TestDeployedApplicationIsCleanedBeforeTransactionalDeletion(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "delete-project", Name: "Delete", CreatedAt: now}
	server := core.Server{ID: "delete-server", Name: "Delete", Address: "/config", Runtime: core.ServerRuntimeKubernetes, State: "ready", AgentMode: "direct", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config", Context: "test"}, CreatedAt: now}
	app := core.App{ID: "delete-app", ProjectID: project.ID, ServerID: server.ID, Name: "Delete", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/chart", State: "ready", CreatedAt: now}
	deployment := core.Deployment{ID: "delete-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	if err := data.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: deployment.ID, Level: "info", Message: "live", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	executor := &observingCleanupExecutor{data: data}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, executor), false, AuthConfig{AdminToken: "secret"}, logger)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/apps/"+app.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected application deletion to return 204, got %d: %s", response.Code, response.Body.String())
	}
	if !executor.cleaned {
		t.Fatal("expected deployed resources to be cleaned before deletion")
	}
	if _, err := data.GetApp(ctx, app.ID); err != store.ErrNotFound {
		t.Fatalf("expected application record removed, got %v", err)
	}
	logs, err := data.ListDeploymentLogs(ctx, deployment.ID, 0)
	if err != nil || len(logs) != 0 {
		t.Fatalf("expected deployment history removed, got logs=%d err=%v", len(logs), err)
	}
}

func TestApplicationDeleteRejectsActivePreviewBeforeCleanup(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "preview-project", Name: "Preview delete", CreatedAt: now}
	server := core.Server{ID: "preview-server", Name: "Preview delete", Address: "/config", Runtime: core.ServerRuntimeKubernetes, State: "ready", AgentMode: "direct", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: "/config", Context: "test"}, CreatedAt: now}
	app := core.App{ID: "preview-app", ProjectID: project.ID, ServerID: server.ID, Name: "Preview delete", BuildType: core.BuildTypeHelm, HelmChart: "oci://registry.example.test/chart", State: "ready", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateDeployment(ctx, core.Deployment{ID: "preview-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	trigger := core.EventTrigger{ID: "preview-trigger", AppID: app.ID, Provider: core.EventProviderGitHub, Repository: "acme/preview", Command: "/preview", Enabled: true, CreatedAt: now, UpdatedAt: now}
	if _, _, err := data.CreateEventTrigger(ctx, trigger); err != nil {
		t.Fatal(err)
	}
	event := core.IncomingEvent{ID: "preview-event", Provider: core.EventProviderGitHub, DeliveryID: "preview-delivery", Kind: core.EventKindPullRequestComment, Action: "created", Repository: "acme/preview", PullRequestNumber: 9, Actor: "operator", ActorAssociation: "MEMBER", TrustedActor: true, Command: "/preview", ReceivedAt: now}
	result, err := data.ProcessIncomingEvent(ctx, event)
	if err != nil || len(result.Previews) != 1 {
		t.Fatalf("expected active preview, got %#v err=%v", result, err)
	}
	executor := &observingCleanupExecutor{data: data}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, executor), false, AuthConfig{AdminToken: "secret"}, logger)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/apps/"+app.ID, nil))
	if response.Code != http.StatusConflict || !bytes.Contains(response.Body.Bytes(), []byte("Close and clean every preview")) {
		t.Fatalf("expected active preview conflict, got %d: %s", response.Code, response.Body.String())
	}
	if executor.cleaned {
		t.Fatal("cleanup must not run before active preview validation")
	}
	if _, err := data.GetApp(ctx, app.ID); err != nil {
		t.Fatalf("preview template must be preserved: %v", err)
	}
}

type observingCleanupExecutor struct {
	data    store.Store
	cleaned bool
}

func (e *observingCleanupExecutor) Deploy(context.Context, core.Deployment, core.App, core.Server, deploy.Progress) error {
	return nil
}

func (e *observingCleanupExecutor) Cleanup(ctx context.Context, app core.App, _ core.Server, _ deploy.Progress) error {
	if _, err := e.data.GetApp(ctx, app.ID); err != nil {
		return err
	}
	e.cleaned = true
	return nil
}

func testKubeconfig(t *testing.T, currentContext string, contexts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	contents := "apiVersion: v1\nkind: Config\ncurrent-context: " + currentContext + "\ncontexts:\n"
	for _, contextName := range contexts {
		contents += "  - name: " + contextName + "\n    context:\n      cluster: cluster\n      user: user\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProjectAndServerCanBeEditedAndDeleted(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/projects", bytes.NewBufferString(`{"name":"Platform","description":"Initial"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected project creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var project core.Project
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/projects/"+project.ID, bytes.NewBufferString(`{"name":"Services","description":"Updated"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected project update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatal(err)
	}
	if project.Name != "Services" || project.Description != "Updated" {
		t.Fatalf("unexpected updated project: %#v", project)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/servers", bytes.NewBufferString(`{"name":"build-01","address":"10.0.0.8","runtime":"docker"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected server creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	var server core.Server
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/servers/"+server.ID, bytes.NewBufferString(`{"name":"build-east","address":"10.0.0.9"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected server update to return 200, got %d: %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&server); err != nil {
		t.Fatal(err)
	}
	if server.Name != "build-east" || server.Address != "10.0.0.9" {
		t.Fatalf("unexpected updated server: %#v", server)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/servers/"+server.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected server deletion to return 204, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodDelete, "/api/v1/projects/"+project.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("expected project deletion to return 204, got %d: %s", response.Code, response.Body.String())
	}
}

func TestProjectAndServerDeleteRejectsReferencedResources(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{AdminToken: "secret"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/api/v1/projects/" + overview.Projects[0].ID, "/api/v1/servers/" + overview.Servers[0].ID} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, tokenRequest(http.MethodDelete, target, nil))
		if response.Code != http.StatusConflict {
			t.Fatalf("expected referenced resource deletion to return 409, got %d: %s", response.Code, response.Body.String())
		}
	}
}

func TestFirstRunSetupSupportsBasicAuthentication(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/auth/status", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"setupRequired":true`)) {
		t.Fatalf("expected setup to be required, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unconfigured controller to reject protected requests, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewBufferString(`{"username":"operator","password":"correct horse battery staple"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected setup to return 201, got %d: %s", response.Code, response.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("operator", "correct horse battery staple")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected configured credentials to authenticate, got %d: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewBufferString(`{"username":"operator","password":"correct horse battery staple"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("expected login to return 200, got %d: %s", response.Code, response.Body.String())
	}
	var session struct{ Token string }
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected session token to authenticate, got %d", response.Code)
	}

	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/auth/setup", bytes.NewBufferString(`{"username":"other","password":"another secure password"}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("expected repeat setup to return 409, got %d", response.Code)
	}
}

func TestEnvironmentCredentialsUseBasicAuthentication(t *testing.T) {
	handler, cleanup := testHandler(t, AuthConfig{Username: "admin", Password: "environment password"})
	defer cleanup()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("admin", "environment password")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("expected environment credentials to authenticate, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	request.SetBasicAuth("admin", "wrong password")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected wrong environment password to return 401, got %d", response.Code)
	}
}

func TestSignedPullRequestWebhookCreatesAndClosesPreview(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{WebhookSecret: "hook-secret", DefaultCommand: "/preview"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	triggerBody := bytes.NewBufferString(`{"repository":"acme/checkout"}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+overview.Apps[0].ID+"/event-triggers", triggerBody))
	if response.Code != http.StatusCreated {
		t.Fatalf("expected trigger creation to return 201, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	if len(overview.EventTriggers) != 1 || overview.EventTriggers[0].Repository != "acme/checkout" {
		t.Fatalf("expected overview event inventory, got %#v", overview.EventTriggers)
	}

	comment := []byte(`{"action":"created","repository":{"full_name":"acme/checkout"},"issue":{"number":17,"pull_request":{"url":"https://example.test/pulls/17"}},"comment":{"id":501,"body":"/preview","author_association":"MEMBER","user":{"login":"octo"}}}`)
	request := signedWebhookRequest("hook-secret", "issue_comment", "delivery-comment", comment)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected preview webhook to return 202, got %d: %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, signedWebhookRequest("hook-secret", "issue_comment", "delivery-comment", comment))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"duplicate":true`)) {
		t.Fatalf("expected delivery retry to be idempotent, got %d: %s", response.Code, response.Body.String())
	}

	closed := []byte(`{"action":"closed","repository":{"full_name":"acme/checkout"},"sender":{"login":"octo"},"pull_request":{"number":17,"head":{"ref":"feature/cart","sha":"abc123"},"base":{"ref":"main"}}}`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, signedWebhookRequest("hook-secret", "pull_request", "delivery-close", closed))
	if response.Code != http.StatusAccepted || !bytes.Contains(response.Body.Bytes(), []byte(`"state":"closed"`)) {
		t.Fatalf("expected close webhook to complete cleanup, got %d: %s", response.Code, response.Body.String())
	}
}

func TestEventTriggerDeploymentHooksCanBeUpdated(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{DefaultCommand: "/preview"})
	defer cleanup()

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodGet, "/api/v1/overview", nil))
	var overview core.Overview
	if err := json.NewDecoder(response.Body).Decode(&overview); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPost, "/api/v1/apps/"+overview.Apps[0].ID+"/event-triggers", bytes.NewBufferString(`{"repository":"acme/hooks","preDeployHook":"echo pre"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create event trigger: %d %s", response.Code, response.Body.String())
	}
	var trigger core.EventTrigger
	if err := json.NewDecoder(response.Body).Decode(&trigger); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, tokenRequest(http.MethodPut, "/api/v1/event-triggers/"+trigger.ID, bytes.NewBufferString(`{"command":"/ship-preview","enabled":true,"preDeployHook":"echo build","postDeployHook":"echo publish"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("update event trigger: %d %s", response.Code, response.Body.String())
	}
	if err := json.NewDecoder(response.Body).Decode(&trigger); err != nil {
		t.Fatal(err)
	}
	if trigger.Command != "/ship-preview" || trigger.PreDeployHook != "echo build" || trigger.PostDeployHook != "echo publish" {
		t.Fatalf("unexpected updated trigger: %#v", trigger)
	}
}

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, true, EventConfig{WebhookSecret: "hook-secret"})
	defer cleanup()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events/github", bytes.NewBufferString(`{}`))
	request.Header.Set("X-Hub-Signature-256", "sha256=00")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected invalid signature to return 401, got %d", response.Code)
	}
}

func signedWebhookRequest(secret, event, delivery string, body []byte) *http.Request {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events/github", bytes.NewReader(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-GitHub-Event", event)
	request.Header.Set("X-GitHub-Delivery", delivery)
	return request
}

func tokenRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Header.Set("Authorization", "Bearer secret")
	return request
}

func testHandler(t *testing.T, auth AuthConfig) (http.Handler, func()) {
	return testHandlerWithDemo(t, auth, true)
}

func testHandlerWithDemo(t *testing.T, auth AuthConfig, seedDemo bool) (http.Handler, func()) {
	return testHandlerWithEventConfig(t, auth, seedDemo, EventConfig{})
}

func testHandlerWithEventConfig(t *testing.T, auth AuthConfig, seedDemo bool, eventConfig EventConfig) (http.Handler, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := data.Migrate(ctx); err != nil {
		cancel()
		_ = data.Close()
		t.Fatal(err)
	}
	if seedDemo {
		if err := data.SeedDemo(ctx); err != nil {
			cancel()
			_ = data.Close()
			t.Fatal(err)
		}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := New(data, deploy.NewService(data, deploy.SimulationExecutor{Delay: time.Millisecond}), seedDemo, auth, logger, eventConfig)
	return handler, func() { cancel(); _ = data.Close() }
}

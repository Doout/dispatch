package hostedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/api"
	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/edge"
	"github.com/doout/dispatch/internal/edgeclient"
	"github.com/doout/dispatch/internal/tenancy"
	"github.com/doout/dispatch/internal/workflowrunner"
	"github.com/oklog/ulid/v2"
)

// Use the production runtime, API and encrypted queue. Only the final worker
// executor is a fixture, so a missing adapter cannot pass by invoking it directly.
func TestHostedHelmApplicationAndServiceCompleteThroughEnrolledWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	factory, err := tenancy.NewFactory(tenancy.FactoryConfig{RootDir: filepath.Join(t.TempDir(), "tenants")})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	tenantID := ulid.Make().String()
	runtime, err := factory.Open(ctx, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err = runtime.Store.CreateUser(ctx, core.User{ID: "owner", Username: "owner", DisplayName: "Owner", SystemRole: core.UserRoleOwner, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	auth := &api.HostedAuth{TenantID: tenantID, LoginURL: "https://dispatch.example.test", Authenticate: func(r *http.Request) (core.Identity, error) {
		if r.Header.Get("Authorization") != "Bearer owner-session" {
			return core.Identity{}, errors.New("not a member")
		}
		return core.Identity{ID: "owner", Kind: core.PrincipalUser, SystemRole: core.UserRoleOwner}, nil
	}}
	instance, err := New(ctx, runtime, tenancy.Tenant{ID: tenantID}, auth, "https://team.dispatch.example.test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close()
	server := httptest.NewTLSServer(instance.Handler)
	defer server.Close()
	request := func(method, path string, body any, status int, result any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer owner-session")
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		payload, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("%s %s: %d %s", method, path, response.StatusCode, payload)
		}
		if result != nil {
			if err = json.Unmarshal(payload, result); err != nil {
				t.Fatal(err)
			}
		}
	}
	var project core.Project
	request(http.MethodPost, "/api/v1/projects", map[string]string{"name": "Worker journey"}, 201, &project)
	node := core.PrivateNetwork{ID: "helm-worker", Name: "Helm worker", Driver: edge.DriverAgent, Config: map[string]string{"workflowMode": "tenant", "workflowProjectId": project.ID}, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err = runtime.Store.CreatePrivateNetwork(ctx, node); err != nil {
		t.Fatal(err)
	}
	enrollment, err := edge.RotateCredentials(ctx, runtime.Store, node.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := edgeclient.LoadIdentity(filepath.Join(t.TempDir(), "identity.json"), server.URL, node.ID)
	if err != nil {
		t.Fatal(err)
	}
	client := workflowrunner.Client{HTTP: server.Client(), Identity: identity, Enrollment: enrollment, Mode: "tenant"}
	var mu sync.Mutex
	counts := map[string]int{}
	serviceExists := false
	var requestPassword string
	worker, err := workflowrunner.OpenWorker(filepath.Join(t.TempDir(), "worker"), node.ID, func(_ context.Context, r workflowrunner.Request, _ string, progress func(string)) workflowrunner.Result {
		mu.Lock()
		defer mu.Unlock()
		counts[r.Kind()]++
		if r.ProjectID != project.ID || r.Mode != "tenant" {
			return workflowrunner.Result{State: "failed", Error: "worker received wrong project or mode"}
		}
		switch r.Kind() {
		case "target_inspect":
			if !strings.Contains(r.TargetInspection.Kubeconfig, "fixture-tenant-token") {
				return workflowrunner.Result{State: "failed", Error: "worker did not receive private target credentials"}
			}
			return workflowrunner.Result{State: "succeeded", TargetEvidence: &core.KubernetesTargetEvidence{ClusterUID: "fixture-cluster", NamespaceUID: "fixture-namespace", Version: "v1.36.0", CheckedAt: time.Now().UTC()}}
		case "storage_inspect":
			return workflowrunner.Result{State: "succeeded", Storage: []core.StorageObservation{}}
		case "deployment":
			d := r.Deployment
			if d.Operation != "deploy" || d.App.BuildType != core.BuildTypeHelm || !strings.Contains(d.Kubeconfig, "fixture-tenant-token") {
				return workflowrunner.Result{State: "failed", Error: "worker deployment lost accepted inputs"}
			}
			progress("Worker installed the accepted Helm release")
			return workflowrunner.Result{State: "succeeded", Log: "Worker installed the accepted Helm release", Snapshot: &core.DeploymentSnapshot{TargetID: d.Server.ID, TargetName: d.Server.Name, Runtime: d.Server.Runtime, Namespace: d.App.HelmNamespace, Release: d.App.HelmRelease, Chart: d.App.HelmChart}}
		case "service_inspect":
			p := r.ServiceProvision
			state, id := "absent", ""
			if serviceExists {
				state, id = "ready", "fixture-owned-release"
			}
			return workflowrunner.Result{State: "succeeded", ServiceInspection: &core.ServiceResourceInspection{RunID: p.Request.Run.ID, ProjectID: p.Request.Run.ProjectID, ServerID: p.Server.ID, Provider: "helm", State: state, ResourceID: id, StorageRetained: true}}
		case "service_provision":
			p := r.ServiceProvision
			if p.Request.Password == "" || !strings.Contains(p.Kubeconfig, "fixture-tenant-token") {
				return workflowrunner.Result{State: "failed", Error: "service worker lost private accepted inputs"}
			}
			serviceExists = true
			requestPassword = p.Request.Password
			outputs, err := deploy.ServiceResourceOutputs(p.Request, nil, &p.Spec)
			if err != nil {
				return workflowrunner.Result{State: "failed", Error: err.Error()}
			}
			return workflowrunner.Result{State: "succeeded", Outputs: outputs}
		default:
			return workflowrunner.Result{State: "failed", Error: "unexpected worker operation " + r.Kind()}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if busy, err := client.Poll(ctx, worker); err != nil || busy {
		t.Fatalf("worker enrollment: %v %v", busy, err)
	}
	workerContext, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() {
		for {
			if _, err := client.Poll(workerContext, worker); err != nil {
				workerDone <- err
				return
			}
			select {
			case <-workerContext.Done():
				workerDone <- workerContext.Err()
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
	defer func() {
		stopWorker()
		if err := <-workerDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("worker loop: %v", err)
		}
	}()
	config := `apiVersion: v1
kind: Config
current-context: fixture
clusters:
- name: fixture
  cluster:
    server: https://cluster.example.test
contexts:
- name: fixture
  context:
    cluster: fixture
    user: fixture
    namespace: team
users:
- name: fixture
  user:
    token: fixture-tenant-token
`
	var target core.Server
	request(http.MethodPost, "/api/v1/servers", map[string]any{"name": "Worker cluster", "projectId": project.ID, "runtime": "kubernetes", "kubernetes": map[string]string{"source": "stored", "kubeconfig": config, "context": "fixture", "namespace": "team"}}, 201, &target)
	if target.Kubernetes == nil || target.Kubernetes.Validation == nil || target.Kubernetes.Validation.ClusterUID != "fixture-cluster" {
		t.Fatal("worker target evidence was not saved")
	}
	var app core.App
	request(http.MethodPost, "/api/v1/apps", map[string]any{"projectId": project.ID, "serverId": target.ID, "name": "Worker application", "buildType": "helm", "helmChart": "fixture", "helmRepository": "https://charts.example.test", "helmNamespace": "team", "helmRelease": "fixture-app"}, 201, &app)
	var deployment core.Deployment
	request(http.MethodPost, "/api/v1/apps/"+app.ID+"/deployments", map[string]string{}, 202, &deployment)
	for {
		deployment, err = runtime.Store.GetDeployment(ctx, deployment.ID)
		if err != nil {
			t.Fatal(err)
		}
		if deployment.State == core.DeploymentSucceeded {
			break
		}
		if deployment.State == core.DeploymentFailed || deployment.State == core.DeploymentCancelled {
			t.Fatalf("Helm deployment failed: %+v", deployment)
		}
		select {
		case <-ctx.Done():
			t.Fatal("Helm deployment did not finish", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	var template core.SavedServiceTemplate
	document := fmt.Sprintf("apiVersion: dispatch/v1alpha1\nkind: ServiceTemplate\nmetadata: {name: worker-database}\nspec:\n  serviceType: postgresql\n  provision:\n    helm: {serverRef: %s, namespace: team}\n", target.ID)
	request(http.MethodPost, "/api/v1/service-templates", map[string]string{"projectId": project.ID, "document": document}, 201, &template)
	var run core.ServiceProvisionRun
	request(http.MethodPost, "/api/v1/service-templates/"+template.ID+"/runs", map[string]string{"name": "worker-database"}, 202, &run)
	for {
		run, err = runtime.Store.GetServiceProvisionRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.State == "succeeded" {
			break
		}
		if run.State == "failed" {
			t.Fatalf("Helm service failed: %+v", run)
		}
		select {
		case <-ctx.Done():
			t.Fatal("Helm service did not finish", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	var inspection core.ServiceResourceInspection
	request(http.MethodPost, "/api/v1/service-provision-runs/"+run.ID+"/resource/inspect", nil, 200, &inspection)
	if inspection.State != "ready" || inspection.ResourceID != "fixture-owned-release" {
		t.Fatalf("service inspection: %+v", inspection)
	}
	service, err := runtime.Store.GetService(ctx, run.ServiceID)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if service.Fields["password"].EncryptedValue == "" || service.Fields["password"].Value != "" || requestPassword == "" {
		t.Fatal("worker service credentials were not protected")
	}
	if counts["target_inspect"] != 1 || counts["deployment"] != 1 || counts["storage_inspect"] < 2 || counts["service_inspect"] < 3 || counts["service_provision"] != 1 {
		t.Fatalf("missing worker operation in complete journey: %v", counts)
	}
}

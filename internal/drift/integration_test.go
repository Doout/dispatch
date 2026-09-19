package drift_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/drift"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHelmDriftReapplyIntegration(t *testing.T) { testHelmDriftIntegration(t, false) }
func TestHelmLegacyDriftIntegration(t *testing.T)  { testHelmDriftIntegration(t, true) }
func testHelmDriftIntegration(t *testing.T, legacy bool) {
	config := os.Getenv("DISPATCH_DRIFT_KUBECONFIG")
	if config == "" {
		t.Skip("set DISPATCH_DRIFT_KUBECONFIG to a disposable Kubernetes cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "drift.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err = data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key")
	os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600)
	vault, err := secretcrypto.OpenFile(key)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "drift-" + strings.ToLower(ulid.Make().String())
	now := time.Now().UTC()
	server := core.Server{ID: "server", Name: "isolated cluster", Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: config, Namespace: namespace}, CreatedAt: now}
	chart := t.TempDir()
	os.Mkdir(filepath.Join(chart, "templates"), 0700)
	os.WriteFile(filepath.Join(chart, "Chart.yaml"), []byte("apiVersion: v2\nname: drift-check\nversion: 0.1.0\n"), 0600)
	os.WriteFile(filepath.Join(chart, "templates", "resources.yaml"), []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
data:
  ordinary: deployed-value
  password: saved-private-value
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: workload
spec:
  replicas: 0
  selector:
    matchLabels: {app: drift-example}
  template:
    metadata:
      labels: {app: drift-example}
    spec:
      containers:
        - name: workload
          image: registry.k8s.io/pause:3.10
`), 0600)
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "Add drift fixture"}} {
		command := exec.Command("git", args...)
		command.Dir = chart
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture repository: %s %v", output, err)
		}
	}
	app := core.App{ID: "application", ProjectID: "project", ServerID: server.ID, Name: "drift-check", BuildType: core.BuildTypeHelm, SourceRepo: "file://" + chart, Branch: "main", HelmChart: ".", HelmNamespace: namespace, HelmRelease: "drift-check", CreatedAt: now}
	binding := core.ServiceBinding{Alias: "db", Helm: &core.ServiceHelmBinding{Keys: map[string]string{"password": "password"}, SecretNameValues: []string{"database.existingSecret"}}}
	if !legacy {
		app.ServiceRuntime = []core.ServiceRuntimeBinding{{Binding: binding, Values: map[string]string{"password": "original-service-credential"}}}
	}
	for _, err := range []error{data.CreateProject(ctx, core.Project{ID: app.ProjectID, Name: app.ProjectID, CreatedAt: now}), data.CreateServer(ctx, server), data.CreateApp(ctx, app)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	d := core.Deployment{ID: ulid.Make().String(), AppID: app.ID, CommitSHA: "chart", State: core.DeploymentQueued, CreatedAt: now, SpecDigest: app.SpecDigest()}
	if err = data.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := drift.New(data, vault)
	var executor deploy.Executor = deploy.HelmExecutor{Capture: s.Capture}
	if legacy {
		executor = deploy.SnapshotExecutor{Next: deploy.HelmExecutor{}, Store: data}
	}
	conn, err := drift.Connect(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		conn.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}).Delete(cleanup, namespace, metav1.DeleteOptions{})
	}()
	if err = executor.Deploy(ctx, d, app, server, func(core.DeploymentState, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if legacy {
		d, err = data.GetDeployment(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		finished := time.Now().UTC()
		d.FinishedAt = &finished
		if _, err := data.GetDriftBaseline(ctx, d.ID); err == nil {
			t.Fatal("legacy deployment unexpectedly captured a baseline")
		}
	}
	d.State = core.DeploymentSucceeded
	if err = data.UpdateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	check, err := s.Check(ctx, app.ID)
	if err != nil || check.State != "synced" {
		t.Fatalf("initial check: %+v %v", check, err)
	}
	maps := conn.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}).Namespace(namespace)
	workloads := conn.Dynamic.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}).Namespace(namespace)
	secrets := conn.Dynamic.Resource(schema.GroupVersionResource{Version: "v1", Resource: "secrets"}).Namespace(namespace)
	secretName := "dispatch-svc-" + strings.ToLower(d.ID) + "-0"
	if _, err = workloads.Patch(ctx, "workload", types.MergePatchType, []byte(`{"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"workload","image":"registry.k8s.io/pause:3.9"}]}}}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = maps.Patch(ctx, "settings", types.MergePatchType, []byte(`{"data":{"password":"external-private-value"}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	check, err = s.Check(ctx, app.ID)
	if err != nil || check.State != "out_of_sync" {
		t.Fatal(check, err)
	}
	encoded, _ := json.Marshal(check)
	for _, secret := range []string{"saved-private-value", "external-private-value", "original-service-credential"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("sensitive value in drift response")
		}
	}
	if err = maps.Delete(ctx, "settings", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if legacy {
		check, err = s.Check(ctx, app.ID)
		if err != nil || check.State != "out_of_sync" || check.Health != "degraded" {
			t.Fatalf("missing legacy resource: %+v %v", check, err)
		}
		check, err = s.Reapply(ctx, app.ID, d.ID, "integration-operator")
		if err != nil || check.State != "synced" {
			t.Fatalf("legacy recovery: %+v %v", check, err)
		}
		return
	}
	if err = secrets.Delete(ctx, secretName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	// A newer failed deployment and changed registration must not replace the baseline.
	failed := d
	failed.ID = ulid.Make().String()
	failed.CreatedAt = now.Add(time.Second)
	failed.State = core.DeploymentFailed
	if err = data.CreateDeployment(ctx, failed); err != nil {
		t.Fatal(err)
	}
	app.HelmValues = "replicas: 99"
	app.ServiceRuntime[0].Values["password"] = "rotated-but-not-deployed"
	if err = data.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	check, err = s.Reapply(ctx, app.ID, d.ID, "integration-operator")
	if err != nil || check.State != "synced" {
		t.Fatalf("reapply: %+v %v", check, err)
	}
	secret, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dataFields, ok := secret.Object["data"].(map[string]any)
	if !ok || len(dataFields) != 1 || dataFields["password"] != base64.StdEncoding.EncodeToString([]byte("original-service-credential")) {
		t.Fatal("reapply did not preserve deployed credentials")
	}
	workload, err := workloads.Get(ctx, "workload", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if workload.Object["spec"].(map[string]any)["replicas"] != int64(0) {
		t.Fatal("replica drift not restored")
	}
	// A resource adopted by another release must never be overwritten.
	if _, err = maps.Patch(ctx, "settings", types.MergePatchType, []byte(`{"metadata":{"annotations":{"meta.helm.sh/release-name":"someone-else"}}}`), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Reapply(ctx, app.ID, d.ID, "operator"); err == nil {
		t.Fatal("overwrote another release")
	}
}

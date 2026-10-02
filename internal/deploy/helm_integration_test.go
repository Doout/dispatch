package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/doout/dispatch/internal/runtimecontract"
	"github.com/doout/dispatch/internal/runtimecontract/conformance"
	"github.com/oklog/ulid/v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// This opt-in test creates and deletes its own namespace on a disposable
// cluster. It never uses the developer's default kubeconfig.
func TestKubernetesLifecycleIntegration(t *testing.T) {
	path := os.Getenv("DISPATCH_TEST_KUBECONFIG")
	if path == "" || os.Getenv("DISPATCH_TEST_KUBERNETES_ISOLATED") != "1" {
		t.Skip("set DISPATCH_TEST_KUBECONFIG and DISPATCH_TEST_KUBERNETES_ISOLATED=1 for a disposable cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal("load isolated target", err)
	}
	connection, err := clientcmd.NewNonInteractiveClientConfig(*configuration, configuration.CurrentContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kubernetes.NewForConfig(connection)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "dispatch-conformance-" + strings.ToLower(ulid.Make().String())
	created, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Labels: map[string]string{"dispatch.test/owner": namespace}}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		current, err := admin.CoreV1().Namespaces().Get(cleanupCtx, namespace, metav1.GetOptions{})
		if err == nil && current.UID == created.UID && current.Labels["dispatch.test/owner"] == namespace {
			if err := admin.CoreV1().Namespaces().Delete(cleanupCtx, namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &created.UID}}); err != nil {
				t.Error("remove test namespace", err)
			}
		}
	})
	config := core.KubernetesServerConfig{KubeconfigPath: path, Context: configuration.CurrentContext, Namespace: namespace}
	evidence, err := kubeconfig.InspectTarget(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	config.Validation = &evidence
	server := core.Server{ID: "integration-cluster", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &config}
	prepared, cleanup, err := prepareKubernetesServer(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	t.Logf("Validated isolated Kubernetes %s", evidence.Version)
	fixture := func(t *testing.T) (core.App, core.Deployment, func() *sdkHelmClient) {
		name := "workload-" + strings.ToLower(ulid.Make().String())
		chartPath := writeKubernetesIntegrationChart(t)
		app := core.App{ID: name, ProjectID: "integration", BuildType: core.BuildTypeHelm, HelmRelease: name, HelmNamespace: namespace, HelmChart: chartPath}
		deployment := core.Deployment{ID: "accepted-" + name, CommitSHA: strings.Repeat("a", 40), SpecDigest: strings.Repeat("b", 64)}
		deployment.Health.Policy.TimeoutSeconds = 90
		client := func() *sdkHelmClient {
			value, err := newSDKHelmClient(prepared, namespace, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			return value.(*sdkHelmClient)
		}
		return app, deployment, client
	}
	t.Run("shared runtime contract", func(t *testing.T) {
		conformance.Run(t, func(t *testing.T) conformance.Fixture {
			app, deployment, client := fixture(t)
			manifest := (HelmExecutor{}).RuntimeCapabilities(app, prepared)
			mutations := 0
			return conformance.Fixture{Manifest: manifest, MutationCount: func() int { return mutations }, ResourceCount: func() int {
				items, err := admin.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{FieldSelector: "metadata.name=" + app.HelmRelease})
				if err != nil {
					t.Fatal(err)
				}
				return len(items.Items)
			}, Execute: func(operationCtx context.Context, op runtimecontract.Operation) error {
				if err := manifest.Check(operationCtx, op); err != nil {
					return err
				}
				var err error
				if op == runtimecontract.Destroy {
					err = client().Uninstall(operationCtx, app.HelmRelease, app)
				} else {
					err = client().UpgradeInstall(operationCtx, app.HelmRelease, app, deployment, nil)
				}
				if err == nil {
					mutations++
				}
				return err
			}}
		})
	})
	t.Run("readiness logs restart and retained rollback", func(t *testing.T) {
		app, deployment, client := fixture(t)
		if err := client().UpgradeInstall(ctx, app.HelmRelease, app, deployment, nil); err != nil {
			t.Fatal(err)
		}
		observed, err := admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
		if err != nil || observed.Status.ReadyReplicas != 1 || observed.Spec.Template.Spec.Containers[0].Image != kubernetesIntegrationImage {
			t.Fatal("ready immutable workload missing", err)
		}
		var provenance helmDeploymentMetadata
		if err := json.Unmarshal([]byte(observed.Annotations[helmProvenanceAnnotation]), &provenance); err != nil || provenance.SpecDigest != deployment.SpecDigest || provenance.ChartCommitSHA != deployment.CommitSHA || provenance.DeploymentID != deployment.ID {
			t.Fatal("accepted deployment evidence missing", err)
		}
		pods, err := admin.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + app.HelmRelease})
		if err != nil || len(pods.Items) != 1 {
			t.Fatal("owned pod missing", err)
		}
		stream, err := admin.CoreV1().Pods(namespace).GetLogs(pods.Items[0].Name, &corev1.PodLogOptions{Container: "app"}).Stream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		logs, err := io.ReadAll(io.LimitReader(stream, 4096))
		stream.Close()
		if err != nil || !strings.Contains(string(logs), "deployment-ready") {
			t.Fatal("pod logs unavailable", err)
		}
		// A new SDK client recovers the retained release and upgrades one workload.
		deployment.ID = "replacement-" + app.ID
		if err := client().UpgradeInstall(ctx, app.HelmRelease, app, deployment, map[string]interface{}{"revision": "second"}); err != nil {
			t.Fatal(err)
		}
		rollback := action.NewRollback(client().configuration)
		rollback.Version = 1
		rollback.DisableHooks = true
		rollback.Wait = true
		rollback.Timeout = 90 * time.Second
		if err := rollback.Run(app.HelmRelease); err != nil {
			t.Fatal(err)
		}
		observed, err = admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
		if err != nil || observed.Spec.Template.Annotations["test-revision"] != "first" {
			t.Fatal("retained rollback failed", err)
		}
		if err := client().Uninstall(ctx, app.HelmRelease, app); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cancellation after partial apply recovers retained release", func(t *testing.T) {
		app, deployment, client := fixture(t)
		if err := client().UpgradeInstall(ctx, app.HelmRelease, app, deployment, nil); err != nil {
			t.Fatal(err)
		}
		interrupted := deployment
		interrupted.ID = "interrupted-" + app.ID
		interrupted.Health.Policy.TimeoutSeconds = 20
		started := time.Now()
		operation, stop := context.WithCancel(ctx)
		defer stop()
		result := make(chan error, 1)
		candidate := client()
		go func() {
			result <- candidate.UpgradeInstall(operation, app.HelmRelease, app, interrupted, map[string]interface{}{
				"revision": "interrupted", "readinessPath": "/not-ready", "partialResource": true, "partialPod": true,
			})
		}()
		// Observe both writes before cancellation, so this exercises partial apply,
		// rather than only rejecting an already-cancelled request.
		err := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 45*time.Second, true, func(ctx context.Context) (bool, error) {
			observed, err := admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			_, err = admin.CoreV1().ConfigMaps(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			_, err = admin.CoreV1().Pods(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return observed.Spec.Template.Annotations["test-revision"] == "interrupted" && err == nil, err
		})
		stop()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("candidate did not report the requested cancellation", err)
			}
		case <-time.After(2 * time.Minute):
			t.Fatal("cancelled upgrade did not complete atomic rollback")
		}
		if err != nil {
			t.Fatal("candidate never reached partial apply", err)
		}
		observed, err := admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
		if err != nil || observed.Spec.Template.Annotations["test-revision"] != "first" || observed.Status.ReadyReplicas != 1 {
			t.Fatal("healthy retained revision was not restored", err)
		}
		if _, err := admin.CoreV1().ConfigMaps(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("partial candidate resource survived rollback", err)
		}
		if _, err := admin.CoreV1().Pods(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("partial candidate pod survived rollback", err)
		}
		history, err := action.NewHistory(client().configuration).Run(app.HelmRelease)
		if err != nil || len(history) != 3 || history[len(history)-1].Info.Status != release.StatusDeployed {
			t.Fatal("retained history did not record recovery", err)
		}
		failed := false
		for _, revision := range history {
			failed = failed || revision.Info.Status == release.StatusFailed
		}
		if !failed {
			t.Fatal("interrupted revision missing from retained history")
		}
		deployment.ID = "recovered-" + app.ID
		if err := client().UpgradeInstall(ctx, app.HelmRelease, app, deployment, map[string]interface{}{"revision": "recovered"}); err != nil {
			t.Fatal("fresh client could not continue after interrupted upgrade", err)
		}
		recoveredHistory, err := action.NewHistory(client().configuration).Run(app.HelmRelease)
		if err != nil || len(recoveredHistory) != 4 {
			t.Fatalf("unexpected recovered history: revisions=%d err=%v", len(recoveredHistory), err)
		}
		// Wait past the cancelled attempt's original readiness deadline. A late
		// SDK worker must not add another rollback after recovery has succeeded.
		settled := time.NewTimer(time.Until(started.Add(35 * time.Second)))
		defer settled.Stop()
		select {
		case <-settled.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		history, err = action.NewHistory(client().configuration).Run(app.HelmRelease)
		if err != nil || len(history) != len(recoveredHistory) {
			t.Fatalf("cancelled attempt changed recovered history after returning: before=%d after=%d err=%v", len(recoveredHistory), len(history), err)
		}
		observed, err = admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
		if err != nil || observed.Spec.Template.Annotations["test-revision"] != "recovered" || observed.Status.ReadyReplicas != 1 {
			t.Fatal("late cancelled work changed the recovered deployment", err)
		}
		if err := client().Uninstall(ctx, app.HelmRelease, app); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cancelled partial installation leaves no late work", func(t *testing.T) {
		app, deployment, client := fixture(t)
		deployment.Health.Policy.TimeoutSeconds = 20
		started := time.Now()
		operation, stop := context.WithCancel(ctx)
		defer stop()
		result := make(chan error, 1)
		candidate := client()
		go func() {
			result <- candidate.UpgradeInstall(operation, app.HelmRelease, app, deployment, map[string]interface{}{
				"readinessPath": "/not-ready", "partialResource": true, "partialPod": true,
			})
		}()
		applied := wait.PollUntilContextTimeout(ctx, 200*time.Millisecond, 45*time.Second, true, func(ctx context.Context) (bool, error) {
			_, err := admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			_, err = admin.CoreV1().Pods(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return err == nil, err
		})
		stop()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("partial install did not report the requested cancellation", err)
			}
		case <-time.After(2 * time.Minute):
			t.Fatal("partial install did not finish atomic cleanup")
		}
		if applied != nil {
			t.Fatal("initial install never reached partial apply", applied)
		}
		if _, err := action.NewHistory(client().configuration).Run(app.HelmRelease); !errors.Is(err, driver.ErrReleaseNotFound) {
			t.Fatal("cancelled initial release was retained", err)
		}
		if _, err := admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("partial initial deployment survived cancellation", err)
		}
		if _, err := admin.CoreV1().Pods(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("partial initial pod survived cancellation", err)
		}
		if _, err := admin.CoreV1().ConfigMaps(namespace).Get(ctx, app.HelmRelease+"-partial", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatal("partial initial configmap survived cancellation", err)
		}
		deployment.ID = "after-cancelled-install-" + app.ID
		deployment.Health.Policy.TimeoutSeconds = 90
		if err := client().UpgradeInstall(ctx, app.HelmRelease, app, deployment, nil); err != nil {
			t.Fatal("fresh client could not install after cancellation", err)
		}
		settled := time.NewTimer(time.Until(started.Add(35 * time.Second)))
		defer settled.Stop()
		select {
		case <-settled.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		history, err := action.NewHistory(client().configuration).Run(app.HelmRelease)
		if err != nil || len(history) != 1 || history[0].Info.Status != release.StatusDeployed {
			t.Fatal("cancelled install changed replacement history after returning", err)
		}
		observed, err := admin.AppsV1().Deployments(namespace).Get(ctx, app.HelmRelease, metav1.GetOptions{})
		if err != nil || observed.Status.ReadyReplicas != 1 {
			t.Fatal("cancelled install removed the replacement workload", err)
		}
		var provenance helmDeploymentMetadata
		if err := json.Unmarshal([]byte(observed.Annotations[helmProvenanceAnnotation]), &provenance); err != nil || provenance.DeploymentID != deployment.ID {
			t.Fatal("replacement deployment provenance changed", err)
		}
		if err := client().Uninstall(ctx, app.HelmRelease, app); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cleanup retains persistent volume claim", func(t *testing.T) {
		app, deployment, client := fixture(t)
		if err := client().UpgradeInstall(ctx, app.HelmRelease, app, deployment, map[string]interface{}{"retainedData": true}); err != nil {
			t.Fatal(err)
		}
		if err := client().Uninstall(ctx, app.HelmRelease, app); err != nil {
			t.Fatal(err)
		}
		claim, err := admin.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, app.HelmRelease+"-data", metav1.GetOptions{})
		if err != nil || claim.Annotations["helm.sh/resource-policy"] != "keep" {
			t.Fatal("protected data disappeared", err)
		}
	})
}

const kubernetesIntegrationImage = "busybox@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

func writeKubernetesIntegrationChart(t *testing.T) string {
	t.Helper()
	chartPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(chartPath, "templates"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chartPath, "Chart.yaml"), []byte("apiVersion: v2\nname: lifecycle\nversion: 1.0.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
spec:
  replicas: 1
  selector:
    matchLabels: {app: {{ .Release.Name }}}
  template:
    metadata:
      labels: {app: {{ .Release.Name }}}
      annotations:
        test-revision: {{ .Values.revision | default "first" | quote }}
    spec:
      containers:
      - name: app
        image: ` + kubernetesIntegrationImage + `
        command: [sh, -c, "mkdir -p /www; echo ready > /www/index.html; if [ -d /data ]; then test -f /data/proof || echo {{ .Values.dataProof | default "retained-proof" }} > /data/proof; cat /data/proof; fi; echo deployment-ready; exec httpd -f -p 8080 -h /www"]
        readinessProbe:
          httpGet: {path: {{ .Values.readinessPath | default "/" | quote }}, port: 8080}
          periodSeconds: 1
        {{- if .Values.retainedData }}
        volumeMounts:
        - {name: retained, mountPath: /data}
      volumes:
      - name: retained
        persistentVolumeClaim: {claimName: {{ .Release.Name }}-data}
        {{- end }}
{{- if .Values.retainedData }}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: {{ .Release.Name }}-data
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests: {storage: 1Mi}
{{- end }}
{{- if .Values.partialResource }}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}-partial
data:
  revision: interrupted
{{- end }}
{{- if .Values.partialPod }}
---
apiVersion: v1
kind: Pod
metadata:
  name: {{ .Release.Name }}-partial
spec:
  terminationGracePeriodSeconds: 0
  containers:
  - name: partial
    image: ` + kubernetesIntegrationImage + `
    command: [sh, -c, "sleep 300"]
    readinessProbe:
      exec: {command: [sh, -c, "exit 1"]}
      periodSeconds: 1
{{- end }}
`
	if err := os.WriteFile(filepath.Join(chartPath, "templates", "workload.yaml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	return chartPath
}

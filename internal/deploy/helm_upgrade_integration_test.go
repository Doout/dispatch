package deploy

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/kubeconfig"
	"github.com/oklog/ulid/v2"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/release"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type kubernetesUpgradeRecord struct {
	Evidence      core.KubernetesTargetEvidence `json:"evidence"`
	Namespace     string                        `json:"namespace"`
	App           core.App                      `json:"app"`
	Deployment    core.Deployment               `json:"deployment"`
	PVCUID        types.UID                     `json:"pvcUid"`
	DeploymentUID types.UID                     `json:"deploymentUid"`
	Proof         string                        `json:"proof"`
}

// This two-phase opt-in test leaves one owned namespace between invocations.
// The caller upgrades its disposable cluster between before and after phases.
// The record contains identities and chart metadata, never kubeconfig credentials.
func TestKubernetesUpgradeIntegration(t *testing.T) {
	path, phase, recordPath := os.Getenv("DISPATCH_TEST_KUBECONFIG"), os.Getenv("DISPATCH_TEST_KUBERNETES_UPGRADE_PHASE"), os.Getenv("DISPATCH_TEST_KUBERNETES_UPGRADE_RECORD")
	if path == "" || phase == "" || recordPath == "" || os.Getenv("DISPATCH_TEST_KUBERNETES_ISOLATED") != "1" {
		t.Skip("requires an isolated cluster, explicit kubeconfig, upgrade phase and private record path")
	}
	if phase != "before" && phase != "after" {
		t.Fatal("upgrade phase must be before or after")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	configuration, err := clientcmd.LoadFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := clientcmd.NewNonInteractiveClientConfig(*configuration, configuration.CurrentContext, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
	if err != nil {
		t.Fatal(err)
	}
	admin, err := kubernetes.NewForConfig(connection)
	if err != nil {
		t.Fatal(err)
	}
	var record kubernetesUpgradeRecord
	if phase == "before" {
		record.Namespace = "dispatch-upgrade-" + strings.ToLower(ulid.Make().String())
		created, err := admin.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: record.Namespace, Labels: map[string]string{"dispatch.test/owner": record.Namespace}}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		record.Evidence.NamespaceUID = string(created.UID)
		t.Cleanup(func() {
			if t.Failed() {
				cleanupUpgradeNamespace(t, admin, record)
			}
		})
		name := "upgrade-" + strings.ToLower(ulid.Make().String())
		record.App = core.App{ID: name, ProjectID: "integration", BuildType: core.BuildTypeHelm, HelmRelease: name, HelmNamespace: record.Namespace}
		record.Deployment = core.Deployment{ID: "accepted-" + name, CommitSHA: strings.Repeat("a", 40), SpecDigest: strings.Repeat("b", 64)}
		record.Deployment.Health.Policy.TimeoutSeconds = 90
		record.Proof = "proof-" + strings.ToLower(ulid.Make().String())
	} else {
		payload, err := os.ReadFile(recordPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(payload, &record); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(record.Namespace, "dispatch-upgrade-") || record.Evidence.NamespaceUID == "" || record.Evidence.ClusterUID == "" || record.App.HelmNamespace != record.Namespace || record.Proof == "" {
			t.Fatal("invalid isolated upgrade record")
		}
		ns, err := admin.CoreV1().Namespaces().Get(ctx, record.Namespace, metav1.GetOptions{})
		if err != nil || string(ns.UID) != record.Evidence.NamespaceUID || ns.Labels["dispatch.test/owner"] != record.Namespace {
			t.Fatal("retained namespace identity changed", err)
		}
		t.Cleanup(func() { cleanupUpgradeNamespace(t, admin, record) })
	}
	config := core.KubernetesServerConfig{KubeconfigPath: path, Context: configuration.CurrentContext, Namespace: record.Namespace}
	current, err := kubeconfig.InspectTarget(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if phase == "before" {
		if !strings.HasPrefix(current.Version, "v1.35.") {
			t.Fatal("before phase requires Kubernetes 1.35", current.Version)
		}
		record.Evidence = current
	} else if !strings.HasPrefix(record.Evidence.Version, "v1.35.") || !strings.HasPrefix(current.Version, "v1.36.") {
		t.Fatal("after phase requires the recorded 1.35 target upgraded to 1.36", record.Evidence.Version, current.Version)
	}
	// Keep the original registration evidence across the cluster upgrade.
	config.Validation = &record.Evidence
	server, cleanup, err := prepareKubernetesServer(ctx, core.Server{ID: "upgrade-integration", Runtime: core.ServerRuntimeKubernetes, Kubernetes: &config})
	if err != nil {
		t.Fatal("registered target could not recover after restart", err)
	}
	defer cleanup()
	record.App.HelmChart = writeKubernetesIntegrationChart(t)
	newClient := func() *sdkHelmClient {
		value, err := newSDKHelmClient(server, record.Namespace, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return value.(*sdkHelmClient)
	}
	if phase == "before" {
		if err := newClient().UpgradeInstall(ctx, record.App.HelmRelease, record.App, record.Deployment, map[string]interface{}{"retainedData": true, "dataProof": record.Proof}); err != nil {
			t.Fatal(err)
		}
		claim, err := admin.CoreV1().PersistentVolumeClaims(record.Namespace).Get(ctx, record.App.HelmRelease+"-data", metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		record.PVCUID = claim.UID
		workload, err := admin.AppsV1().Deployments(record.Namespace).Get(ctx, record.App.HelmRelease, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		record.DeploymentUID = workload.UID
		assertUpgradeDataProof(t, ctx, admin, record)
		record.App.HelmChart = "" // The next process recreates its own chart directory.
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(recordPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(payload); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("Retained accepted deployment and data on %s for the upgrade phase", current.Version)
		return
	}
	claim, err := admin.CoreV1().PersistentVolumeClaims(record.Namespace).Get(ctx, record.App.HelmRelease+"-data", metav1.GetOptions{})
	if err != nil || claim.UID != record.PVCUID {
		t.Fatal("retained PVC identity changed", err)
	}
	workload, err := admin.AppsV1().Deployments(record.Namespace).Get(ctx, record.App.HelmRelease, metav1.GetOptions{})
	if err != nil || workload.UID != record.DeploymentUID {
		t.Fatal("retained deployment identity changed", err)
	}
	history, err := action.NewHistory(newClient().configuration).Run(record.App.HelmRelease)
	if err != nil || len(history) != 1 || history[0].Info.Status != release.StatusDeployed {
		t.Fatal("accepted Helm history was not preserved", err)
	}
	var provenance helmDeploymentMetadata
	if err := json.Unmarshal([]byte(workload.Annotations[helmProvenanceAnnotation]), &provenance); err != nil || provenance.DeploymentID != record.Deployment.ID || provenance.SpecDigest != record.Deployment.SpecDigest || provenance.ChartCommitSHA != record.Deployment.CommitSHA {
		t.Fatal("accepted provenance changed during cluster upgrade", err)
	}
	updated := record.Deployment
	updated.ID = "after-cluster-upgrade-" + record.App.ID
	if err := newClient().UpgradeInstall(ctx, record.App.HelmRelease, record.App, updated, map[string]interface{}{"revision": "after-upgrade", "retainedData": true, "dataProof": "must-not-replace-original-proof"}); err != nil {
		t.Fatal("new client could not upgrade retained release", err)
	}
	assertUpgradeDataProof(t, ctx, admin, record)
	rollback := action.NewRollback(newClient().configuration)
	rollback.Version, rollback.DisableHooks, rollback.Wait, rollback.Timeout = 1, true, true, 90*time.Second
	if err := rollback.Run(record.App.HelmRelease); err != nil {
		t.Fatal("pre-cluster-upgrade release could not be restored", err)
	}
	assertUpgradeDataProof(t, ctx, admin, record)
	history, err = action.NewHistory(newClient().configuration).Run(record.App.HelmRelease)
	if err != nil || len(history) != 3 || history[2].Info.Status != release.StatusDeployed {
		t.Fatal("post-upgrade history missing upgrade and retained rollback", err)
	}
	if err := newClient().Uninstall(ctx, record.App.HelmRelease, record.App); err != nil {
		t.Fatal(err)
	}
	claim, err = admin.CoreV1().PersistentVolumeClaims(record.Namespace).Get(ctx, record.App.HelmRelease+"-data", metav1.GetOptions{})
	if err != nil || claim.UID != record.PVCUID || claim.Annotations["helm.sh/resource-policy"] != "keep" {
		t.Fatal("post-upgrade cleanup removed protected PVC", err)
	}
	t.Logf("Verified %s to %s with stable cluster, namespace, workload, PVC, accepted provenance and Helm history", record.Evidence.Version, current.Version)
}

func assertUpgradeDataProof(t *testing.T, ctx context.Context, admin kubernetes.Interface, record kubernetesUpgradeRecord) {
	t.Helper()
	pods, err := admin.CoreV1().Pods(record.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + record.App.HelmRelease})
	if err != nil {
		t.Fatal(err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
			continue
		}
		stream, err := admin.CoreV1().Pods(record.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{Container: "app"}).Stream(ctx)
		if err != nil {
			t.Fatal(err)
		}
		logs, err := io.ReadAll(io.LimitReader(stream, 4096))
		stream.Close()
		if err != nil || !strings.Contains(string(logs), record.Proof) || strings.Contains(string(logs), "must-not-replace-original-proof") {
			t.Fatal("original volume data was not preserved", err)
		}
		return
	}
	t.Fatal("no running owned workload to verify retained data")
}

func cleanupUpgradeNamespace(t *testing.T, admin kubernetes.Interface, record kubernetesUpgradeRecord) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	current, err := admin.CoreV1().Namespaces().Get(ctx, record.Namespace, metav1.GetOptions{})
	if err == nil && string(current.UID) == record.Evidence.NamespaceUID && current.Labels["dispatch.test/owner"] == record.Namespace {
		if err := admin.CoreV1().Namespaces().Delete(ctx, record.Namespace, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &current.UID}}); err != nil {
			t.Error("remove upgrade test namespace", err)
		}
	}
}

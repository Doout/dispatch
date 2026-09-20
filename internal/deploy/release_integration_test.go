package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/store"
	"github.com/oklog/ulid/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReleasePreviewAndRollbackIntegration(t *testing.T) {
	kubeconfig := os.Getenv("DISPATCH_RELEASE_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("set DISPATCH_RELEASE_KUBECONFIG to a disposable Kubernetes cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "releases.db"))
	must(err)
	defer data.Close()
	must(data.Migrate(ctx))
	key := filepath.Join(t.TempDir(), "master.key")
	must(os.WriteFile(key, []byte(strings.Repeat("!", 32)), 0600))
	vault, err := secretcrypto.OpenFile(key)
	must(err)
	now := time.Now().UTC()
	name := "release-test-" + strings.ToLower(ulid.Make().String())
	project := core.Project{ID: name, Name: name, CreatedAt: now}
	must(data.CreateProject(ctx, project))
	server := core.Server{ID: name, Name: name, Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: kubeconfig}, CreatedAt: now}
	must(data.CreateServer(ctx, server))
	kube, err := serviceKubeClient(server)
	must(err)
	_, err = kube.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{})
	must(err)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = kube.CoreV1().Namespaces().Delete(cleanup, name, metav1.DeleteOptions{})
	})
	repo := t.TempDir()
	serviceFixtureRepo(t, repo, map[string]string{
		"chart/Chart.yaml":  "apiVersion: v2\nname: release-test\nversion: 0.1.0\n",
		"chart/values.yaml": "color: blue\ndatabase: {}\n",
		"chart/templates/config.yaml": `{{ if eq .Values.color "invalid" }}{{ fail "intentional rendering failure" }}{{ end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .Release.Name }}
data:
  color: {{ .Values.color | quote }}
  credentialSecret: {{ .Values.database.existingSecret | quote }}
  credentialKey: {{ .Values.database.urlKey | quote }}
`,
	})
	app := core.App{ID: name, ProjectID: name, ServerID: name, Name: "release-test", SourceRepo: "file://" + repo, Branch: "main", BuildType: core.BuildTypeHelm, HelmChart: "chart", HelmNamespace: name, HelmRelease: "release-test", HelmValues: "color: blue", CreatedAt: now}
	must(data.CreateApp(ctx, app))
	cipher, err := vault.Encrypt("service:"+name+":url", []byte("original-connection-value"))
	must(err)
	service := core.Service{ID: name, Name: "database", ProjectID: name, Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"url": {Sensitive: true, Configured: true, EncryptedValue: cipher}}, CreatedAt: now, UpdatedAt: now}
	must(data.CreateService(ctx, service))
	binding := core.ServiceBinding{Alias: "db", ServiceRef: name, Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "url"}, SecretNameValues: []string{"database.existingSecret"}, KeyValues: map[string]string{"database.urlKey": "url"}}}
	must(data.ReplaceAppServiceBindings(ctx, name, []core.ServiceBinding{binding}))
	s := NewService(data, SnapshotExecutor{Next: HelmExecutor{}, Store: data})
	s.ConfigureServices(vault, nil)
	wait := func(d core.Deployment) core.Deployment {
		t.Helper()
		for !d.State.Terminal() {
			select {
			case <-ctx.Done():
				t.Fatal("deployment timed out")
			case <-time.After(50 * time.Millisecond):
			}
			d, err = data.GetDeployment(ctx, d.ID)
			must(err)
		}
		return d
	}
	first, err := s.Start(ctx, name, "")
	must(err)
	first = wait(first)
	if first.State != core.DeploymentSucceeded {
		t.Fatalf("first release failed: %s", first.Message)
	}
	original, err := kube.CoreV1().Secrets(name).Get(ctx, serviceSecretName(first.ID, 0), metav1.GetOptions{})
	must(err)
	if string(original.Data["url"]) != "original-connection-value" {
		t.Fatal("original runtime credential missing")
	}
	app.HelmValues = "color: green"
	must(data.UpdateApp(ctx, app))
	cipher, err = vault.Encrypt("service:"+name+":url", []byte("updated-connection-value"))
	must(err)
	service.Fields["url"] = core.ServiceField{Sensitive: true, Configured: true, EncryptedValue: cipher}
	service.Revision = 2
	must(data.UpdateService(ctx, service, 1))
	preview := s.PreviewRelease(ctx, app, server, "", SourceAuthExecutor{})
	if !preview.Ready {
		t.Fatalf("preview failed: %#v", preview.Checks)
	}
	raw, _ := json.Marshal(preview)
	if strings.Contains(string(raw), "connection-value") {
		t.Fatal("preview exposed credential")
	}
	config, err := kube.CoreV1().ConfigMaps(name).Get(ctx, app.HelmRelease, metav1.GetOptions{})
	must(err)
	if config.Data["color"] != "blue" {
		t.Fatal("preview changed runtime resource")
	}
	secrets, err := kube.CoreV1().Secrets(name).List(ctx, metav1.ListOptions{LabelSelector: "dispatch.service-binding=true"})
	must(err)
	if len(secrets.Items) != 1 {
		t.Fatal("preview persisted credential Secret")
	}
	second, err := s.Start(ctx, name, "")
	must(err)
	second = wait(second)
	if second.State != core.DeploymentSucceeded {
		t.Fatalf("second release failed: %s", second.Message)
	}
	app.HelmValues = "color: invalid"
	must(data.UpdateApp(ctx, app))
	failed, err := s.Start(ctx, name, "")
	must(err)
	failed = wait(failed)
	if failed.State != core.DeploymentFailed {
		t.Fatal("invalid chart unexpectedly succeeded")
	}
	config, err = kube.CoreV1().ConfigMaps(name).Get(ctx, app.HelmRelease, metav1.GetOptions{})
	must(err)
	if config.Data["color"] != "green" {
		t.Fatal("failed deployment replaced previous release")
	}
	review, err := s.PreviewRollback(ctx, first.ID)
	must(err)
	if !review.Available {
		t.Fatal("retained rollback unavailable: " + review.Message)
	}
	rolled, err := s.StartRollback(ctx, first.ID, second.ID, "test-operator", nil)
	must(err)
	rolled = wait(rolled)
	if rolled.State != core.DeploymentSucceeded {
		t.Fatal("rollback failed: " + rolled.Message)
	}
	config, err = kube.CoreV1().ConfigMaps(name).Get(ctx, app.HelmRelease, metav1.GetOptions{})
	must(err)
	if config.Data["color"] != "blue" || config.Data["credentialSecret"] != serviceSecretName(first.ID, 0) {
		t.Fatal("rollback did not restore exact chart inputs and original credential reference")
	}
	original, err = kube.CoreV1().Secrets(name).Get(ctx, serviceSecretName(first.ID, 0), metav1.GetOptions{})
	must(err)
	if string(original.Data["url"]) != "original-connection-value" {
		t.Fatal("rollback mutated retained credentials")
	}
	if _, err = kube.CoreV1().Secrets(name).Get(ctx, serviceSecretName(second.ID, 0), metav1.GetOptions{}); err != nil {
		t.Fatal("rollback removed retained newer credential")
	}
}

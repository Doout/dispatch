package deploy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// These tests use explicitly supplied disposable infrastructure. No existing
// Dispatch application or release is modified.
func TestServiceDockerRuntimeIntegration(t *testing.T) {
	url := os.Getenv("DISPATCH_SERVICES_DOCKER_POSTGRES_URL")
	if url == "" {
		t.Skip("set DISPATCH_SERVICES_DOCKER_POSTGRES_URL to a database reachable from Docker's default network")
	}
	for _, build := range []core.BuildType{core.BuildTypeDockerfile, core.BuildTypeCompose} {
		t.Run(string(build), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			repo := t.TempDir()
			files := map[string]string{
				"Dockerfile":   "FROM postgres:17-alpine\nCMD [\"sh\",\"-c\",\"psql \\\"$DATABASE_URL\\\" -tAc 'SELECT 1' > /tmp/connected && sleep 300\"]\n",
				"compose.yaml": "services:\n  api:\n    network_mode: bridge\n    image: postgres:17-alpine\n    command: [sh, -c, \"psql \\\"$$DATABASE_URL\\\" -tAc 'SELECT 1' > /tmp/connected && sleep 300\"]\n  unrelated:\n    network_mode: bridge\n    image: postgres:17-alpine\n    command: [sleep, '300']\n"}
			serviceFixtureRepo(t, repo, files)
			app := core.App{ID: "service-integration-" + strings.ToLower(ulid.Make().String()), Name: "service-integration", SourceRepo: "file://" + repo, Branch: "main", BuildType: build, ContextPath: ".", DockerfilePath: "Dockerfile", ComposePath: "compose.yaml"}
			binding := core.ServiceBinding{Alias: "db", Environment: map[string]string{"DATABASE_URL": "connectionUrl"}}
			if build == core.BuildTypeCompose {
				binding.Environment = nil
				binding.Compose = map[string]map[string]string{"api": {"DATABASE_URL": "connectionUrl"}}
			}
			app.ServiceRuntime = []core.ServiceRuntimeBinding{{Binding: binding, Values: map[string]string{"connectionUrl": url}, SensitiveValues: []string{url}}}
			executor := DockerExecutor{}
			server := core.Server{Address: "local", Name: "local", Runtime: "docker"}
			progress := func(core.DeploymentState, string) error { return nil }
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				if err := executor.Cleanup(cleanup, app, server, progress); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			})
			deployment := core.Deployment{ID: ulid.Make().String()}
			if err := executor.Deploy(ctx, deployment, app, server, progress); err != nil {
				t.Fatal(redactServiceMessage(err.Error(), app.ServiceRuntime))
			}
			name := dockerResourceName(app.ID)
			if build == core.BuildTypeCompose {
				name += "-api-1"
			}
			deadline := time.Now().Add(20 * time.Second)
			connected := false
			var lastOutput []byte
			var lastErr error
			for time.Now().Before(deadline) {
				output, err := exec.CommandContext(ctx, "docker", "exec", name, "cat", "/tmp/connected").CombinedOutput()
				lastOutput, lastErr = output, err
				if err == nil && strings.TrimSpace(string(output)) == "1" {
					connected = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !connected {
				logs, _ := exec.CommandContext(ctx, "docker", "logs", name).CombinedOutput()
				t.Fatalf("application did not connect to PostgreSQL using the service binding: logs=%s exec=%s error=%v", redactServiceMessage(string(logs), app.ServiceRuntime), redactServiceMessage(string(lastOutput), app.ServiceRuntime), lastErr)
			}
			if build == core.BuildTypeCompose {
				output, err := exec.CommandContext(ctx, "docker", "exec", dockerResourceName(app.ID)+"-unrelated-1", "sh", "-c", "test -z \"$DATABASE_URL\"").CombinedOutput()
				if err != nil {
					t.Fatalf("credential delivered to unrelated Compose container: %s", output)
				}
			}
			if build == core.BuildTypeDockerfile {
				t.Cleanup(func() {
					exec.Command("docker", "image", "rm", "dispatch/service-integration:"+strings.ToLower(deployment.ID)).Run()
				})
			}
		})
	}
}
func serviceFixtureRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-b", "main"}, {"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "Add database connectivity example"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture repository: %s %v", output, err)
		}
	}
}
func TestServiceHelmRuntimeIntegration(t *testing.T) {
	config := os.Getenv("DISPATCH_SERVICES_TEST_KUBECONFIG")
	url := os.Getenv("DISPATCH_SERVICES_KUBE_POSTGRES_URL")
	if config == "" || url == "" {
		t.Skip("set DISPATCH_SERVICES_TEST_KUBECONFIG and DISPATCH_SERVICES_KUBE_POSTGRES_URL for a disposable cluster")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	repo := t.TempDir()
	serviceFixtureRepo(t, repo, map[string]string{
		"chart/Chart.yaml":  "apiVersion: v2\nname: connection-check\nversion: 0.1.0\n",
		"chart/values.yaml": "database: {}\n",
		"chart/templates/deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .Release.Name }}
spec:
  replicas: 1
  selector:
    matchLabels: { app: {{ .Release.Name }} }
  template:
    metadata:
      labels: { app: {{ .Release.Name }} }
    spec:
      containers:
        - name: check
          image: postgres:17-alpine
          imagePullPolicy: IfNotPresent
          command: [sh, -c, 'psql "$DATABASE_URL" -tAc "SELECT 1" > /tmp/connected && sleep 600']
          env:
            - name: DATABASE_URL
              valueFrom:
                secretKeyRef:
                  name: {{ .Values.database.existingSecret }}
                  key: {{ .Values.database.urlKey }}
          readinessProbe:
            exec:
              command: [sh, -c, 'test "$(cat /tmp/connected)" = 1']
            initialDelaySeconds: 1
            periodSeconds: 1
`})
	namespace := "service-test-" + strings.ToLower(ulid.Make().String())
	app := core.App{ID: namespace, Name: "connection-check", SourceRepo: "file://" + repo, Branch: "main", BuildType: core.BuildTypeHelm, HelmChart: "chart", HelmNamespace: namespace, HelmRelease: "connection-check"}
	app.ServiceRuntime = []core.ServiceRuntimeBinding{{Binding: core.ServiceBinding{Alias: "db", Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "connectionUrl"}, SecretNameValues: []string{"database.existingSecret"}, KeyValues: map[string]string{"database.urlKey": "url"}}}, Values: map[string]string{"connectionUrl": url}, SensitiveValues: []string{url}}}
	server := core.Server{Name: "test", Runtime: "kubernetes", Kubernetes: &core.KubernetesServerConfig{KubeconfigPath: config}}
	client, err := serviceKubeClient(server)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client.CoreV1().Namespaces().Delete(cleanup, namespace, metav1.DeleteOptions{})
	})
	executor := HelmExecutor{}
	progress := func(core.DeploymentState, string) error { return nil }
	first := core.Deployment{ID: ulid.Make().String()}
	if err = executor.Deploy(ctx, first, app, server, progress); err != nil {
		t.Fatal(redactServiceMessage(err.Error(), app.ServiceRuntime))
	}
	pods, err := client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) != 1 || !pods.Items[0].Status.ContainerStatuses[0].Ready {
		t.Fatal("application did not become ready after connecting to PostgreSQL", err)
	}
	// A second release gets a different Secret while the first one remains
	// available to Helm history and rollback.
	second := core.Deployment{ID: ulid.Make().String()}
	if err = executor.Deploy(ctx, second, app, server, progress); err != nil {
		t.Fatal(redactServiceMessage(err.Error(), app.ServiceRuntime))
	}
	if _, err = client.CoreV1().Secrets(namespace).Get(ctx, serviceSecretName(first.ID, 0), metav1.GetOptions{}); err != nil {
		t.Fatal("previous release Secret removed", err)
	}
	manifest, err := HelmReleaseManifest(ctx, server, namespace, app.HelmRelease)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(manifest, url) {
		t.Fatal("credential was embedded into Helm manifest")
	}
	if err = executor.Cleanup(ctx, app, server, progress); err != nil {
		t.Fatal(err)
	}
	secrets, err := client.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: "dispatch.service-binding=true"})
	if err != nil || len(secrets.Items) != 0 {
		t.Fatal(fmt.Sprintf("owned secrets remain after release cleanup: %d %v", len(secrets.Items), err))
	}
}

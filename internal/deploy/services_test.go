package deploy

import (
	"context"
	"encoding/json"
	"errors"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/serviceconn"
	"github.com/doout/dispatch/internal/store"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestServiceRuntimeFiles(t *testing.T) {
	dir := t.TempDir()
	binding := core.ServiceRuntimeBinding{Binding: core.ServiceBinding{Alias: "db", Environment: map[string]string{"DATABASE_URL": "url"}}, Values: map[string]string{"url": "postgresql://user:p$a@host/db"}}
	path, err := dockerServiceEnv(dir, []core.ServiceRuntimeBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	bytes, _ := os.ReadFile(path)
	if stat.Mode().Perm() != 0600 || string(bytes) != "DATABASE_URL=postgresql://user:p$a@host/db\n" {
		t.Fatal("incorrect runtime environment")
	}
	binding.Values["url"] = "line1\nline2"
	if _, err = dockerServiceEnv(dir, []core.ServiceRuntimeBinding{binding}); err == nil {
		t.Fatal("accepted multiline Docker environment")
	}
	compose := filepath.Join(dir, "compose.yaml")
	os.WriteFile(compose, []byte("services:\n  api:\n    image: postgres:17-alpine\n  worker:\n    image: postgres:17-alpine\n"), 0600)
	binding.Binding.Environment = nil
	binding.Binding.Compose = map[string]map[string]string{"api": {"DATABASE_URL": "url"}}
	binding.Values["url"] = "literal${PASSWORD}\nsecond line"
	path, err = composeServiceOverride(dir, compose, []core.ServiceRuntimeBinding{binding})
	if err != nil {
		t.Fatal(err)
	}
	bytes, _ = os.ReadFile(path)
	var doc struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err = yaml.Unmarshal(bytes, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Services) != 1 || doc.Services["api"].Environment["DATABASE_URL"] != "literal$${PASSWORD}\nsecond line" {
		t.Fatalf("invalid Compose override: %s", bytes)
	}
	binding.Binding.Compose["missing"] = map[string]string{"URL": "url"}
	if _, err = composeServiceOverride(dir, compose, []core.ServiceRuntimeBinding{binding}); err == nil {
		t.Fatal("accepted unknown Compose service")
	}
}
func TestHelmServiceSecretIsolationAndValueMappings(t *testing.T) {
	ctx := context.Background()
	client := fake.NewClientset()
	app := core.App{ID: "application", ServiceRuntime: []core.ServiceRuntimeBinding{{Binding: core.ServiceBinding{Alias: "db", Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "connectionUrl"}, SecretNameValues: []string{"database.existingSecret"}, KeyValues: map[string]string{"database.urlKey": "url"}}}, Values: map[string]string{"connectionUrl": "postgresql://user:old@host/db"}}}}
	first := core.Deployment{ID: "first"}
	if err := createServiceSecrets(ctx, client, first, app, "test", "release"); err != nil {
		t.Fatal(err)
	}
	app.ServiceRuntime[0].Values["connectionUrl"] = "postgresql://user:new@host/db"
	second := core.Deployment{ID: "second"}
	if err := createServiceSecrets(ctx, client, second, app, "test", "release"); err != nil {
		t.Fatal(err)
	}
	old, err := client.CoreV1().Secrets("test").Get(ctx, serviceSecretName(first.ID, 0), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if string(old.Data["url"]) != "postgresql://user:old@host/db" || old.Immutable == nil || !*old.Immutable {
		t.Fatal("new deployment changed previous Secret")
	}
	values := map[string]interface{}{}
	if err = helmServiceValues(second, app, values); err != nil {
		t.Fatal(err)
	}
	rendered, _ := yaml.Marshal(values)
	if strings.Contains(string(rendered), "postgresql://") || !strings.Contains(string(rendered), serviceSecretName(second.ID, 0)) || !strings.Contains(string(rendered), "urlKey: url") {
		t.Fatalf("bad Helm binding values: %s", rendered)
	}
	foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "test"}, Data: map[string][]byte{"keep": []byte("yes")}}
	client.CoreV1().Secrets("test").Create(ctx, foreign, metav1.CreateOptions{})
	// Secret data never enters values even when a destination happens to have a
	// name that does not trigger the legacy sensitive-path heuristics.
	if err = applyServiceValue(map[string]interface{}{"database": "scalar"}, "database.secret", "name"); err == nil {
		t.Fatal("accepted scalar parent")
	}
}

type heldBindingStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
}

func (s *heldBindingStore) GetDeploymentServiceBindings(ctx context.Context, id string) ([]core.CapturedServiceBinding, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.Store.GetDeploymentServiceBindings(ctx, id)
}

type serviceRecordingExecutor struct{ seen chan string }

func (e serviceRecordingExecutor) Deploy(_ context.Context, _ core.Deployment, app core.App, _ core.Server, progress Progress) error {
	value := app.ServiceRuntime[0].Values["password"]
	e.seen <- value
	progress(core.DeploymentStarting, "Connecting with "+value)
	return errors.New("connection failure: " + value)
}
func TestQueuedDeploymentUsesCapturedCredentialsAndRedactsErrors(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
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
	now := time.Now()
	data.CreateProject(ctx, core.Project{ID: "p", Name: "p", CreatedAt: now})
	data.CreateServer(ctx, core.Server{ID: "s", Name: "s", Runtime: "docker", Address: "local", CreatedAt: now})
	app := core.App{ID: "app", ProjectID: "p", ServerID: "s", Name: "app", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	if err = data.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	cipher, _ := vault.Encrypt(serviceconn.FieldAAD("db", "password"), []byte("original-private-password"))
	connection := core.Service{ID: "db", ProjectID: "p", Name: "db", Type: "generic", Revision: 1, Fields: map[string]core.ServiceField{"password": {Sensitive: true, Configured: true, EncryptedValue: cipher}}}
	if err = data.CreateService(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err = data.ReplaceAppServiceBindings(ctx, app.ID, []core.ServiceBinding{{Alias: "db", ServiceRef: "db", Environment: map[string]string{"PASSWORD": "password"}}}); err != nil {
		t.Fatal(err)
	}
	held := &heldBindingStore{Store: data, entered: make(chan struct{}), release: make(chan struct{})}
	seen := make(chan string, 1)
	service := NewService(held, serviceRecordingExecutor{seen})
	service.ConfigureServices(vault, nil)
	deployment, err := service.Start(ctx, app.ID, "commit")
	if err != nil {
		t.Fatal(err)
	}
	<-held.entered
	cipher, _ = vault.Encrypt(serviceconn.FieldAAD("db", "password"), []byte("replacement-private-password"))
	connection.Fields["password"] = core.ServiceField{Sensitive: true, Configured: true, EncryptedValue: cipher}
	connection.Revision = 2
	if err = data.UpdateService(ctx, connection, 1); err != nil {
		t.Fatal(err)
	}
	close(held.release)
	select {
	case value := <-seen:
		if value != "original-private-password" {
			t.Fatal("queued deployment used updated credential")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deployment did not run")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, err := data.GetDeployment(ctx, deployment.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.State.Terminal() {
			logs, _ := data.ListDeploymentLogs(ctx, deployment.ID, 0)
			payload, _ := json.Marshal(logs)
			if strings.Contains(current.Message, "original-private-password") || strings.Contains(string(payload), "original-private-password") {
				t.Fatal("service credential appeared in deployment evidence")
			}
			if current.Snapshot.ServiceBindings[0].Revision != 1 {
				t.Fatal("snapshot revision changed")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("deployment did not finish")
}

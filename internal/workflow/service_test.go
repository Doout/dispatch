package workflow

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

func TestSyncSourceKeepsLastValidResourceSet(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "sync.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	var version atomic.Int32
	version.Store(1)
	valid := func(command string) string {
		return `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: example
spec:
  sources:
    app:
      repository: owner/app
  jobs:
    build:
      runFrom: app
      run: ` + command + "\n"
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC()})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/config/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": fmt.Sprintf("config-%d", version.Load())})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/app/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "app-1"})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/owner/config/git/trees/config-"):
			entries := []map[string]any{{"path": ".dispatch/app.yaml", "type": "blob", "sha": "config-blob", "size": 256}}
			if version.Load() >= 5 {
				entries[0]["path"] = ".dispatch/slots.template.yaml"
				if version.Load() != 6 {
					entries = append(entries, map[string]any{"path": "values/slots/example.yaml", "type": "blob", "sha": "slot-blob", "size": 16})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": entries})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/config/git/blobs/config-blob":
			contents := valid("./build-v1.sh")
			if version.Load() == 2 {
				contents = "kind: Application\nspec:\n  unknown: true\n"
			} else if version.Load() == 3 {
				contents = valid("./build-v2.sh")
			}
			if version.Load() >= 5 {
				application := strings.Replace(valid("./build-v2.sh"), "name: example", "name: ${slot.name}", 1)
				contents = "apiVersion: dispatch/v1alpha1\nkind: ApplicationTemplate\nmetadata: {name: slots}\nspec:\n  files: {path: values/slots, pattern: '*.yaml'}\n  template:\n"
				for _, line := range strings.Split(strings.TrimSpace(application), "\n") {
					contents += "    " + line + "\n"
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(contents)), "size": len(contents)})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/config/git/blobs/slot-blob":
			contents := "replicas: 1"
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(contents)), "size": len(contents)})

		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := githubapp.New(data, vault)
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	privateKey, webhookSecret, err := manager.EncryptCredentials("github", string(pemValue), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "project", Name: "Project", CreatedAt: now}
	connection := core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: server.URL, APIURL: server.URL, AppID: 42, InstallationID: 73, WebhookURL: "https://dispatch.example/hook", EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{ID: "config", ProjectID: project.ID, GitHubAppID: connection.ID, Name: "Config", Repository: "owner/config", Branch: "main", Path: ".dispatch", SyncMode: core.ConfigSyncPoll, PollIntervalSeconds: 300, Active: false, State: "syncing", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, manager, nil, nil, nil)
	if _, err := service.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	resources, err := data.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 {
		t.Fatalf("unexpected initial resources: %#v err=%v", resources, err)
	}
	if !resources[0].Active || resources[0].State != "ready" {
		t.Fatalf("new resource was not enabled for automatic deployment: %#v", resources[0])
	}
	initialDigest := resources[0].SpecDigest
	resources[0].Active = true
	resources[0].State = "ready"
	if err := data.UpdateWorkflowResource(ctx, resources[0]); err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	if _, err := service.SyncSource(ctx, source.ID); err == nil {
		t.Fatal("expected invalid repository configuration")
	}
	resources, err = data.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 || resources[0].SpecDigest != initialDigest || !resources[0].Active {
		t.Fatalf("invalid sync replaced the last valid set: %#v err=%v", resources, err)
	}
	version.Store(3)
	if _, err := service.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	resources, err = data.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 || resources[0].SpecDigest == initialDigest || !resources[0].Active {
		t.Fatalf("valid update was not reconciled: %#v err=%v", resources, err)
	}
	if _, err := service.Deactivate(ctx, resources[0].ID); err != nil {
		t.Fatal(err)
	}
	version.Store(4)
	if _, err := service.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	resources, err = data.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 || resources[0].Active || resources[0].State != "paused" {
		t.Fatalf("repository sync did not preserve the explicit pause: %#v err=%v", resources, err)
	}
	stableID := resources[0].ID
	for _, v := range []int32{5, 6, 7} {
		version.Store(v)
		if _, err := service.SyncSource(ctx, source.ID); err != nil {
			t.Fatal(err)
		}
		resources, err = data.ListWorkflowResources(ctx, source.ID)
		if err != nil || len(resources) != 1 || resources[0].ID != stableID || resources[0].Active {
			t.Fatalf("identity or pause lost at version %d: %#v %v", v, resources, err)
		}
		if v == 5 && (resources[0].Path != ".dispatch/slots.template.yaml" || resources[0].State != "paused") {
			t.Fatal(resources)
		}
		if v == 6 && resources[0].State != "removed" {
			t.Fatal("last file removal did not deactivate", resources)
		}
		if v == 7 && resources[0].State != "pending" {
			t.Fatal("removed slot automatically reactivated", resources)
		}
	}

}

func TestPollOnceDetectsRevisionsWithoutWebhooksAndReloadsConfiguration(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "poll.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0o600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	var configVersion, serviceVersion atomic.Int32
	configVersion.Store(1)
	serviceVersion.Store(1)
	var treeReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC()})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example/config/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": fmt.Sprintf("config-%d", configVersion.Load())})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example/service/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": fmt.Sprintf("service-%d", serviceVersion.Load())})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example/ui/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": "ui-1"})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/statuses/"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/example/config/git/trees/config-"):
			treeReads.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []map[string]any{{"path": "deployment/slot1.yaml", "type": "blob", "sha": "config-blob", "size": 512}}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/example/config/git/blobs/config-blob":
			uiPath := ""
			if configVersion.Load() == 2 {
				uiPath = "\n      path: web"
			}
			contents := `apiVersion: dispatch/v1alpha1
kind: Application
metadata:
  name: slot1
spec:
  sources:
    service:
      repository: example/service
    ui:
      repository: example/ui` + uiPath + "\n"
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(contents)), "size": len(contents)})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := githubapp.New(data, vault)
	pemValue := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	privateKey, webhookSecret, err := manager.EncryptCredentials("github", string(pemValue), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	project := core.Project{ID: "poll-project", Name: "Project", CreatedAt: now}
	connection := core.GitHubAppConnection{ID: "github", Name: "GitHub", WebURL: server.URL, APIURL: server.URL, AppID: 42, InstallationID: 73,
		WebhookURL: "https://dispatch.example/hook", EncryptedPrivateKey: privateKey, EncryptedWebhookSecret: webhookSecret, State: "ready", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	source := core.ConfigSource{ID: "poll-config", ProjectID: project.ID, GitHubAppID: connection.ID, Name: "Configuration", Repository: "example/config", Branch: "main",
		Path: "deployment", SyncMode: core.ConfigSyncPoll, PollIntervalSeconds: 60, Active: true, State: "syncing", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	service := NewService(data, manager, nil, nil, nil, t.TempDir())
	if _, err := service.SyncSource(ctx, source.ID); err != nil {
		t.Fatalf("poll-only source required webhook setup: %v", err)
	}
	resources, err := data.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 {
		t.Fatalf("unexpected resources: %#v err=%v", resources, err)
	}
	resource := resources[0]
	if !resource.Active {
		t.Fatal("imported application is not enabled")
	}
	initial, err := data.ListWorkflowRevisions(ctx, resource.ID, 10)
	if err != nil || len(initial) != 1 || initial[0].Trigger != "configuration sync" {
		t.Fatalf("import did not start the initial deployment: %#v err=%v", initial, err)
	}
	if err := service.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	revisions, err := data.ListWorkflowRevisions(ctx, resource.ID, 10)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("unchanged repositories created a revision: %#v err=%v", revisions, err)
	}
	if treeReads.Load() != 1 {
		t.Fatalf("unchanged poll downloaded configuration content; tree reads=%d", treeReads.Load())
	}

	serviceVersion.Store(2)
	makePollDue(t, ctx, data, source.ID)
	if err := service.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	revisions, err = data.ListWorkflowRevisions(ctx, resource.ID, 10)
	if err != nil || len(revisions) != 2 || revisions[0].Sources["service"].CommitSHA != "service-2" || revisions[0].Sources["ui"].CommitSHA != "ui-1" {
		t.Fatalf("service revision was not detected: %#v err=%v", revisions, err)
	}
	if treeReads.Load() != 1 {
		t.Fatalf("source-only change reloaded configuration content; tree reads=%d", treeReads.Load())
	}

	initialDigest := resource.SpecDigest
	configVersion.Store(2)
	makePollDue(t, ctx, data, source.ID)
	if err := service.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	resources, err = data.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 || resources[0].SpecDigest == initialDigest || resources[0].ConfigSHA != "config-2" || !resources[0].Active {
		t.Fatalf("configuration change was not reloaded: %#v err=%v", resources, err)
	}
	revisions, err = data.ListWorkflowRevisions(ctx, resource.ID, 10)
	if err != nil || len(revisions) != 3 || revisions[0].SpecDigest != resources[0].SpecDigest {
		t.Fatalf("configuration-only change did not deploy exactly once: %#v err=%v", revisions, err)
	}
	makePollDue(t, ctx, data, source.ID)
	if err := service.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	revisions, err = data.ListWorkflowRevisions(ctx, resource.ID, 10)
	if err != nil || len(revisions) != 3 {
		t.Fatalf("unchanged poll duplicated a deployment: count=%d err=%v", len(revisions), err)
	}
	if treeReads.Load() != 2 {
		t.Fatalf("configuration was not fetched exactly once per changed revision; tree reads=%d", treeReads.Load())
	}
}

func makePollDue(t *testing.T, ctx context.Context, data *store.SQLStore, sourceID string) {
	t.Helper()
	source, err := data.GetConfigSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	previous := time.Now().Add(-2 * time.Minute).UTC()
	source.LastPolledAt = &previous
	if err := data.UpdateConfigSource(ctx, source); err != nil {
		t.Fatal(err)
	}
}

package api

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
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

func TestRepositoryRecoveryPreservesConfiguredBranchAndHistory(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	var phase atomic.Int32
	var heads atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations":
			fmt.Fprint(w, `[{"id":73,"account":{"login":"org"}}]`)
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			json.NewEncoder(w).Encode(map[string]any{"token": "fixture-token", "expires_at": now.Add(time.Hour)})
		case strings.HasSuffix(r.URL.Path, "/installation"):
			fmt.Fprint(w, `{"id":73,"app_id":42}`)
		case r.URL.Path == "/installation/repositories":
			name := "org/config"
			if phase.Load() > 0 {
				name = "org/current"
			}
			items := []any{}
			if phase.Load() != 3 {
				items = append(items, map[string]any{"id": 42, "full_name": name, "archived": phase.Load() == 2})
			}
			json.NewEncoder(w).Encode(map[string]any{"repositories": items})
		case strings.Contains(r.URL.Path, "/commits/"):
			heads.Add(1)
			if !strings.HasSuffix(r.URL.Path, "/release") {
				t.Error("branch silently changed", r.URL.Path)
			}
			json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("a", 40)})
		case strings.Contains(r.URL.Path, "/git/trees/"):
			fmt.Fprint(w, `{"tree":[{"path":".dispatch/check.yaml","type":"blob","sha":"blob","size":128}]}`)
		case strings.Contains(r.URL.Path, "/git/blobs/"):
			document := "apiVersion: dispatch/v1alpha1\nkind: Pipeline\nmetadata: {name: check}\nspec:\n  jobs:\n    check: {run: 'true'}\n"
			json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(document)), "size": len(document)})
		default:
			t.Error("unexpected request", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer endpoint.Close()
	keyPath := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 31)+"!"), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	manager := githubapp.New(a.store, vault)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, webhook, err := manager.EncryptCredentials("repo-app", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "repository-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.store.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "repo-app", Name: "Repository fixture", AppID: 42, InstallationID: 73, APIURL: endpoint.URL, WebURL: "https://github.example", EncryptedPrivateKey: encrypted, EncryptedWebhookSecret: webhook, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	a.eventConfig.GitHubApps = manager
	a.workflows.GitHub = manager
	projects, _ := a.store.ListProjects(ctx)
	input := configSourceRequest{ProjectID: projects[0].ID, GitHubAppID: "repo-app", Name: "Config", Repository: "org/config", RepositoryID: 42, Branch: "release", Path: ".dispatch", SyncMode: core.ConfigSyncPoll, PollIntervalSeconds: 300}
	raw := serviceRequestTest(t, a, "POST", "/api/v1/config-sources", input, 201)
	var source core.ConfigSource
	json.Unmarshal(raw, &source)
	if source.RepositoryID != 42 || source.State != "ready" {
		t.Fatal(string(raw))
	}
	resources, err := a.store.ListWorkflowResources(ctx, source.ID)
	if err != nil || len(resources) != 1 {
		t.Fatal(resources, err)
	}
	original := resources[0]
	revision := core.WorkflowRevision{ID: "historical-repository-revision", ResourceID: original.ID, ConfigSHA: original.ConfigSHA, SpecDigest: original.SpecDigest, State: "succeeded", Trigger: "manual", Sources: map[string]core.WorkflowSourceRevision{"config": {Repository: "org/config", Branch: "release", CommitSHA: original.ConfigSHA}}, CreatedAt: now, FinishedAt: &now}
	if err = a.store.CreateWorkflowRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	phase.Store(1)
	raw = serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/repository-check", nil, 200)
	json.Unmarshal(raw, &source)
	if source.RepositoryStatus == nil || source.RepositoryStatus.State != "renamed" || source.Repository != "org/config" || source.Branch != "release" {
		t.Fatal(string(raw))
	}
	before := heads.Load()
	serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/sync", nil, 422)
	if heads.Load() != before {
		t.Fatal("blocked source fetched a fallback revision")
	}
	input.Repository = "org/current"
	raw = serviceRequestTest(t, a, "PUT", "/api/v1/config-sources/"+source.ID, input, 200)
	json.Unmarshal(raw, &source)
	if source.Repository != "org/current" || source.RepositoryID != 42 || source.Branch != "release" || source.State != "ready" {
		t.Fatal(string(raw))
	}
	phase.Store(2)
	serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/sync", nil, 422)
	source, _ = a.store.GetConfigSource(ctx, source.ID)
	if source.RepositoryStatus.State != "archived" {
		t.Fatal(source)
	}
	phase.Store(3)
	serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/repository-check", nil, 200)
	source, _ = a.store.GetConfigSource(ctx, source.ID)
	if source.RepositoryStatus.State != "inaccessible" {
		t.Fatal("absence falsely reported deletion", source)
	}
	data := a.store.(*store.SQLStore)
	if err = data.RecordDeletedRepository(ctx, "other-app", 42, "org/current", "other-delivery", now); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/repository-check", nil, 200)
	source, _ = a.store.GetConfigSource(ctx, source.ID)
	if source.RepositoryStatus.State != "inaccessible" {
		t.Fatal("other connection changed source", source)
	}
	if err = data.RecordDeletedRepository(ctx, "repo-app", 42, "org/current", "signed-delivery", now); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/repository-check", nil, 200)
	source, _ = a.store.GetConfigSource(ctx, source.ID)
	if source.RepositoryStatus.State != "deleted" {
		t.Fatal(source)
	}
	retained, _ := a.store.GetWorkflowResource(ctx, original.ID)
	if retained.ConfigSHA != original.ConfigSHA || retained.Document != original.Document {
		t.Fatal("recovery changed historical source evidence")
	}
	history, err := a.store.GetWorkflowRevision(ctx, revision.ID)
	if err != nil || history.ConfigSHA != revision.ConfigSHA || !reflect.DeepEqual(history.Sources, revision.Sources) || history.State != revision.State {
		t.Fatal("recovery changed the accepted revision", history, err)
	}
	phase.Store(1)
	raw = serviceRequestTest(t, a, "POST", "/api/v1/config-sources/"+source.ID+"/sync", nil, 200)
	source = core.ConfigSource{}
	json.Unmarshal(raw, &source)
	if source.State != "ready" || source.RepositoryStatus.State != "accessible" || source.LastError != "" {
		t.Fatal("restored access did not recover", string(raw))
	}
	input.Repository = "https://other.example/org/current"
	serviceRequestTest(t, a, "PUT", "/api/v1/config-sources/"+source.ID, input, 400)

	user := core.User{ID: "repository-member", Username: "repository-member", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "repository-member-role", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: source.ProjectID, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}
	if err = a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	memberRequest := func(method, path string, status int) []byte {
		t.Helper()
		r := tokenRequest(method, "/api/v1/config-sources/"+source.ID+path, nil)
		r.Header.Set("Impersonate-User", user.ID)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("member %s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	memberRequest("GET", "/repositories", 403)
	memberRequest("GET", "/branches?repository=org/current&repositoryId=42", 403)
	memberRequest("POST", "/repository-check", 403)
	grant.Role = core.RoleOperator
	if err = a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	raw = memberRequest("POST", "/repository-check", 200)
	if strings.Contains(string(raw), "repo-app") {
		t.Fatal("access check exposed the connection credential ID")
	}
	memberRequest("GET", "/repositories", 200)
	memberRequest("GET", "/branches?repository=org/another&repositoryId=99", 403)
}

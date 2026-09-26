package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
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
	"github.com/doout/dispatch/internal/deploy"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/doout/dispatch/internal/store"
)

func TestPreviewTemplateGitSourceSyncAndRecovery(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "preview-import.db"))
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
	var revision atomic.Int32
	revision.Store(1)
	var failRead atomic.Bool
	var uiRevision atomic.Int32
	uiRevision.Store(1)
	validDocument := "apiVersion: dispatch/v1alpha1\nkind: WorkflowTemplate\nmetadata:\n  name: preview-{{ instance.id }}\nspec:\n  triggers:\n    pullRequestComment:\n      sources: [service, ui]\n      command: /preview\n  sources:\n    service:\n      repository: example/service\n      ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n    ui:\n      repository: example/ui\n      branch: main\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/app/installations/73/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token", "expires_at": time.Now().Add(time.Hour).UTC()})
		case r.URL.Path == "/repos/example/service/pulls/42":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "open", "head": map[string]string{"ref": "service-feature", "sha": strings.Repeat("a", 40)}, "base": map[string]string{"ref": "main"}})
		case r.URL.Path == "/repos/example/ui/commits/main":
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat("b", 40)})
		case r.URL.Path == "/repos/example/ui/pulls/84" || r.URL.Path == "/repos/example/ui/pulls/85":
			sha := strings.Repeat(string(rune('c'+uiRevision.Load())), 40)
			if strings.HasSuffix(r.URL.Path, "/85") {
				sha = strings.Repeat("f", 40)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "open", "head": map[string]string{"ref": "ui-feature", "sha": sha}, "base": map[string]string{"ref": "main"}})
		case strings.Contains(r.URL.Path, "/statuses/"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/repos/example/devops/commits/main":
			if failRead.Load() {
				http.Error(w, "Contents read access missing", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"sha": strings.Repeat(string(rune('a'+revision.Load())), 40)})
		case strings.Contains(r.URL.Path, "/git/trees/"):
			if !strings.HasSuffix(r.URL.Path, strings.Repeat(string(rune('a'+revision.Load())), 40)) {
				t.Error("file read was not pinned to resolved commit")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"truncated": false, "tree": []map[string]any{{"path": "deployment/templates/preview.yaml", "type": "blob", "sha": "blob", "size": len(validDocument)}}})
		case r.URL.Path == "/repos/example/devops/git/blobs/blob":
			document := validDocument
			if revision.Load() == 2 {
				document = "kind: broken"
			}
			if revision.Load() == 3 {
				document = strings.ReplaceAll(validDocument, "name: preview-", "name: updated-")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(document)), "size": len(document)})
		default:
			t.Errorf("unexpected request %s", r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := githubapp.New(data, vault)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	encryptedKey, encryptedSecret, err := manager.EncryptCredentials("github", string(privateKey), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := data.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "Test", WebURL: server.URL, APIURL: server.URL, AppID: 42, InstallationID: 73, EncryptedPrivateKey: encryptedKey, EncryptedWebhookSecret: encryptedSecret, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	a := New(data, deploy.NewService(data, &previewCleanupRecorder{cleaned: map[string]bool{}}), false, AuthConfig{}, slog.New(slog.NewTextHandler(io.Discard, nil)), EventConfig{GitHubApps: manager})
	if err := data.CreateProject(ctx, core.Project{ID: "project", Name: "Preview", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateConfigSource(ctx, core.ConfigSource{ID: "config", ProjectID: "project", GitHubAppID: "github", Name: "Slots", Repository: "example/devops", Branch: "main", Path: "deployment", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"configSourceId":"config","githubAppId":"github","name":"Dev preview","repository":"example/service","command":"/ignored","previewUrl":"https://dev.example/preview/{{ instance.id }}","active":true,"document":"client must not override GitHub YAML","gitSource":{"repository":"Example/devops","branch":"main","path":"deployment/templates/preview.yaml","commitSha":"untrusted"}}`)
	response := httptest.NewRecorder()
	a.createWorkflowPreviewTemplate(response, httptest.NewRequest(http.MethodPost, "/workflow/preview-templates", bytes.NewReader(input)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}
	var template core.WorkflowPreviewTemplate
	if err := json.NewDecoder(response.Body).Decode(&template); err != nil {
		t.Fatal(err)
	}
	if template.Document != validDocument || template.GitSource.CommitSHA != strings.Repeat("b", 40) {
		t.Fatalf("template was not loaded from pinned GitHub file: %+v", template)
	}
	if len(template.WatchRepositories) != 2 || template.Command != "/preview" {
		t.Fatalf("YAML trigger was not applied: %+v", template)
	}
	targets, err := a.previewPollTargets(ctx)
	if err != nil || len(targets) != 2 {
		t.Fatalf("wrong comment watch targets: %+v %v", targets, err)
	}
	for _, target := range targets {
		if target.repository != "example/service" && target.repository != "example/ui" {
			t.Fatalf("watching dependency comments: %s", target.repository)
		}
	}
	ignored := &previewPollTarget{connectionID: "github", repository: "example/devops", workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	if err := a.processWorkflowPreviewComment(ctx, ignored, core.IncomingEvent{Command: "/preview", PullRequestNumber: 11}, events.GitHubResolver{}); err != nil {
		t.Fatal(err)
	}
	ignoredResources, err := data.ListWorkflowResources(ctx, "")
	if err != nil || len(ignoredResources) != 0 {
		t.Fatal("dependency comment created a preview")
	}
	// Invalid commits keep the last valid definition and block creation. The
	// comment is not reserved, so polling can retry after the file is fixed.
	revision.Store(2)
	if err := a.syncWorkflowPreviewTemplates(ctx); err == nil {
		t.Fatal("invalid template was accepted")
	}
	broken, err := data.GetWorkflowPreviewTemplate(ctx, template.ID)
	if err != nil {
		t.Fatal(err)
	}
	if broken.Document != validDocument || broken.GitSource.CommitSHA != template.GitSource.CommitSHA || broken.GitSource.LastError == "" {
		t.Fatalf("invalid revision replaced last good template: %+v", broken)
	}
	resolver := events.GitHubResolver{BaseURL: server.URL, Client: server.Client()}
	target := &previewPollTarget{connectionID: "github", repository: template.Repository, workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	event := core.IncomingEvent{Repository: template.Repository, PullRequestNumber: 42, Command: "/preview", SourceCommentID: "comment-1", Arguments: "with ui=#84", HeadSHA: strings.Repeat("a", 40), TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err == nil {
		t.Fatal("new preview was created from a template with a sync error")
	}
	resources, err := data.ListWorkflowResources(ctx, "")
	if err != nil || len(resources) != 0 {
		t.Fatalf("blocked comment created a resource: %+v %v", resources, err)
	}
	revision.Store(3)
	if err := a.syncWorkflowPreviewTemplates(ctx); err != nil {
		t.Fatal(err)
	}
	recovered, err := data.GetWorkflowPreviewTemplate(ctx, template.ID)
	if err != nil || recovered.GitSource.LastError != "" || recovered.GitSource.CommitSHA != strings.Repeat("d", 40) || !strings.Contains(recovered.Document, "name: updated-") {
		t.Fatalf("template did not recover: %+v %v", recovered, err)
	}
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	resources, err = data.ListWorkflowResources(ctx, "")
	if err != nil || len(resources) != 1 || resources[0].Name != "updated-42" {
		t.Fatalf("comment did not use latest template: %+v %v", resources, err)
	}
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(triggers) != 1 || triggers[0].TemplateSource == nil || triggers[0].TemplateSource.CommitSHA != recovered.GitSource.CommitSHA {
		t.Fatalf("instance lost template provenance: %+v %v", triggers, err)
	}
	waitForRuns := func(count int) []core.WorkflowRevision {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			runs, err := data.ListWorkflowRevisions(ctx, resources[0].ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			done := len(runs) == count
			for _, run := range runs {
				if run.State == "queued" || run.State == "running" {
					done = false
				}
			}
			if done {
				return runs
			}
			if time.Now().After(deadline) {
				t.Fatal("preview runs did not finish")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	initialRuns := waitForRuns(1)
	if initialRuns[0].Sources["ui"].CommitSHA != strings.Repeat("d", 40) {
		t.Fatal("linked UI PR was not pinned")
	}
	initialID := initialRuns[0].ID
	// A bare command keeps the link and resolves the UI PR's latest head.
	uiRevision.Store(2)
	event.SourceCommentID, event.Arguments = "comment-2", ""
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	runs := waitForRuns(2)
	for _, run := range runs {
		expected := strings.Repeat("e", 40)
		if run.ID == initialID {
			expected = strings.Repeat("d", 40)
		}
		if run.Sources["ui"].CommitSHA != expected {
			t.Fatal("linked PR update changed the wrong run", run.Sources)
		}
	}
	// A later comment changes the link on the same instance.
	event.SourceCommentID, event.Arguments = "comment-3", "with ui=#85"
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	runs = waitForRuns(3)
	foundReplacement := false
	for _, run := range runs {
		if run.Sources["ui"].CommitSHA == strings.Repeat("f", 40) {
			foundReplacement = true
		}
	}
	if !foundReplacement {
		t.Fatal("replacement UI PR was not resolved")
	}
	updatedTriggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || len(updatedTriggers) != 1 || updatedTriggers[0].LinkedPullRequests["ui"] != 85 {
		t.Fatal("replacement UI link was not saved", err)
	}
	instances, err := data.ListWorkflowResources(ctx, "")
	if err != nil || len(instances) != 1 || instances[0].ID != resources[0].ID {
		t.Fatal("link change replaced the instance", err)
	}
	if recovered.Document != strings.ReplaceAll(validDocument, "name: preview-", "name: updated-") {
		t.Fatal("PR overrides changed the reusable definition")
	}
	// A UI comment can be the primary trigger too. The same PR number gets
	// its own instance ID while linking the existing service PR as a source.
	uiTarget := &previewPollTarget{connectionID: "github", repository: "example/ui", workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	uiEvent := core.IncomingEvent{Repository: uiTarget.repository, PullRequestNumber: 42, Command: "/preview", SourceCommentID: "ui-comment", Arguments: "with service=#42", HeadSHA: strings.Repeat("f", 40), TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, uiTarget, uiEvent, resolver); err != nil {
		t.Fatal(err)
	}
	if len(uiTarget.workflowTriggers) != 1 {
		t.Fatal("UI comment did not create an instance")
	}
	uiInstance, err := data.GetWorkflowResource(ctx, uiTarget.workflowTriggers[0].ResourceID)
	if err != nil || !strings.HasPrefix(uiInstance.Name, "updated-42-") {
		t.Fatal("UI/service PR number collision was not isolated", err)
	}
	resources[0] = uiInstance
	uiRuns := waitForRuns(1)
	if uiRuns[0].Sources["service"].CommitSHA != strings.Repeat("a", 40) || uiRuns[0].Sources["ui"].CommitSHA != strings.Repeat("f", 40) {
		t.Fatal("UI primary/service linked commits were not resolved")
	}
	failRead.Store(true)
	if err := a.syncWorkflowPreviewTemplates(ctx); err == nil {
		t.Fatal("missing GitHub access was not reported")
	}
	denied, err := data.GetWorkflowPreviewTemplate(ctx, template.ID)
	if err != nil || denied.GitSource.LastError == "" || denied.Document != recovered.Document {
		t.Fatalf("access failure lost the template: %+v %v", denied, err)
	}
}

func TestPreviewTemplateGitSourceRejectsUnsafePaths(t *testing.T) {
	for _, value := range []string{"../template.yaml", "/template.yaml", "folder/../template.yaml", "folder", ""} {
		if err := validatePreviewTemplateGitSource(&core.WorkflowPreviewTemplateGitSource{Repository: "org/repo", Branch: "main", Path: value}); err == nil {
			t.Errorf("accepted path %q", value)
		}
	}
}

package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/doout/dispatch/internal/events"
	"github.com/doout/dispatch/internal/githubapp"
	"github.com/go-chi/chi/v5"
)

type panelGitHubFixture struct {
	mu          sync.Mutex
	comments    map[string]events.GitHubComment
	permissions map[string]string
	editors     map[string]string
	posts       int
	next        int
	closed      map[string]bool
	open        bool
	created     time.Time
}

func (f *panelGitHubFixture) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/v3")
	switch {
	case strings.HasSuffix(path, "/installation"):
		_ = json.NewEncoder(w).Encode(map[string]int{"id": 73, "app_id": 42})
	case path == "/app/installations/73/access_tokens":
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation", "expires_at": time.Now().Add(time.Hour)})
	case path == "/api/graphql":
		var q struct{ Variables map[string]string }
		_ = json.NewDecoder(r.Body).Decode(&q)
		id := strings.TrimPrefix(q.Variables["id"], "node-")
		c := f.comments[id]
		editor := f.editors[id]
		typ := "User"
		if editor == "dispatch[bot]" {
			typ = "Bot"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"node": map[string]any{"body": c.Body, "updatedAt": c.UpdatedAt, "viewerDidAuthor": true, "editor": map[string]string{"login": editor, "__typename": typ}}}})
	case strings.Contains(path, "/collaborators/"):
		parts := strings.Split(path, "/")
		_ = json.NewEncoder(w).Encode(map[string]string{"permission": f.permissions[parts[len(parts)-2]]})
	case strings.HasSuffix(path, "/pulls"):
		items := []events.GitHubPullRequest{}
		if f.open {
			items = append(items, events.GitHubPullRequest{Number: 42, CreatedAt: f.created})
		}
		_ = json.NewEncoder(w).Encode(items)
	case strings.Contains(path, "/pulls/"):
		state := "open"
		parts := strings.Split(path, "/")
		repo := parts[2] + "/" + parts[3]
		if !f.open || f.closed[repo] {
			state = "closed"
		}
		sha := strings.Repeat("a", 40)
		if strings.Contains(path, "/ui/") {
			sha = strings.Repeat("b", 40)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": state, "head": map[string]string{"ref": "feature", "sha": sha}, "base": map[string]string{"ref": "main"}})
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/issues/comments"):
		parts := strings.Split(path, "/")
		repo := parts[2] + "/" + parts[3]
		items := []events.GitHubComment{}
		for _, c := range f.comments {
			if strings.Contains(c.IssueURL, "/repos/"+repo+"/") {
				items = append(items, c)
			}
		}
		_ = json.NewEncoder(w).Encode(items)
	case r.Method == http.MethodGet && strings.Contains(path, "/issues/comments/"):
		id := path[strings.LastIndex(path, "/")+1:]
		c, ok := f.comments[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(c)
	case r.Method == http.MethodPatch || r.Method == http.MethodPost:
		parts := strings.Split(path, "/")
		if len(parts) < 5 {
			t.Errorf("unexpected mutation %s", path)
			http.NotFound(w, r)
			return
		}
		var input struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&input)
		id := parts[len(parts)-1]
		number := 42
		if r.Method == http.MethodPost {
			f.posts++
			f.next++
			id = strconv.Itoa(f.next)
			number, _ = strconv.Atoi(parts[len(parts)-2])
		}
		c := f.comments[id]
		c.ID = json.Number(id)
		c.NodeID = "node-" + id
		c.Body = input.Body
		c.UpdatedAt = time.Now().UTC().Truncate(time.Second)
		if c.CreatedAt.IsZero() {
			c.CreatedAt = c.UpdatedAt
			c.IssueURL = fmt.Sprintf("%s/api/v3/repos/%s/%s/issues/%d", "http://github.example", parts[2], parts[3], number)
			c.User.Login = "dispatch[bot]"
		}
		f.comments[id] = c
		f.editors[id] = "dispatch[bot]"
		_ = json.NewEncoder(w).Encode(c)
	default:
		t.Errorf("unexpected request %s %s", r.Method, path)
		http.NotFound(w, r)
	}
}

func previewPanelFixture(t *testing.T) (*API, *panelGitHubFixture, core.WorkflowPreviewTemplate) {
	t.Helper()
	ctx := context.Background()
	a, data, _, _ := previewLifetimeFixture(t, "0", nil)
	// Start with a saved template and no preview; the lifetime fixture supplies
	// the project, GitHub connection and repository credentials.
	if err := data.RemoveWorkflowPreviewResource(ctx, "resource", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteApp(ctx, "current"); err != nil {
		t.Fatal(err)
	}
	if err := data.DeleteApp(ctx, "older"); err != nil {
		t.Fatal(err)
	}
	fake := &panelGitHubFixture{comments: map[string]events.GitHubComment{}, permissions: map[string]string{"maintainer": "write", "reader": "read"}, editors: map[string]string{}, next: 100, open: true, created: time.Now().UTC()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fake.serve(t, w, r) }))
	t.Cleanup(server.Close)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte("0123456789abcdef0123456789abcde!"), 0600); err != nil {
		t.Fatal(err)
	}
	vault, err := secretcrypto.OpenFile(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	manager := githubapp.New(data, vault)
	privateKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	encryptedKey, encryptedSecret, err := manager.EncryptCredentials("github", string(privateKey), "0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := data.GetGitHubApp(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	connection.APIURL = server.URL + "/api/v3"
	connection.WebURL = server.URL
	connection.InstallationID = 73
	connection.AppID = 42
	connection.EncryptedPrivateKey = encryptedKey
	connection.EncryptedWebhookSecret = encryptedSecret
	if err := data.UpdateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	a.eventConfig.GitHubApps = manager
	document := `apiVersion: dispatch/v1alpha1
kind: WorkflowTemplate
metadata:
  name: preview-{{ instance.id }}
spec:
  triggers:
    pullRequestComment:
      sources: [service, ui]
      command: /preview
      ttl: 0
  sources:
    service: {repository: example/service, ref: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}
    ui: {repository: example/ui, ref: bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb}
`
	now := time.Now().UTC().Add(-time.Minute)
	template := core.WorkflowPreviewTemplate{ID: "template", ConfigSourceID: "config", GitHubAppID: "github", Name: "Example previews", Repository: "example/service", WatchRepositories: []string{"example/service", "example/ui"}, Command: "/preview", PreviewURL: "https://preview.example/{{ instance.id }}", Document: document, Active: true, CreatedAt: now, UpdatedAt: now}
	if err := data.CreateWorkflowPreviewTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	return a, fake, template
}

func (f *panelGitHubFixture) edit(id, editor, label string, checked bool) events.GitHubComment {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.comments[id]
	from, to := "- [ ] ", "- [x] "
	if !checked {
		from, to = to, from
	}
	c.Body = strings.Replace(c.Body, from+label, to+label, 1)
	c.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	f.comments[id] = c
	f.editors[id] = editor
	return c
}

func TestPreviewPanelOpenOptInAndCheckboxDeployDeduplication(t *testing.T) {
	ctx := context.Background()
	a, fake, template := previewPanelFixture(t)
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil || len(panels) != 0 {
		t.Fatalf("disabled template posted panels: %+v %v", panels, err)
	}
	template.CommentOnOpen = true
	if err := a.store.UpdateWorkflowPreviewTemplate(ctx, template); err != nil {
		t.Fatal(err)
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	panels, err = a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil || len(panels) != 2 {
		t.Fatalf("expected a panel on each new watched PR: %+v %v", panels, err)
	}
	panel := panels[0]
	if !strings.Contains(panel.Body, "No preview is deployed") || panel.ResourceID != "" {
		t.Fatalf("PR opening deployed a preview: %+v", panel)
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	fake.edit(panel.CommentID, "reader", previewRedeployLabel, true)
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	panels, _ = a.store.ListWorkflowPreviewPanels(ctx)
	if panels[0].ResourceID != "" {
		t.Fatal("reader deployed a preview")
	}
	fake.edit(panel.CommentID, "maintainer", previewRedeployLabel, true)
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	panels, _ = a.store.ListWorkflowPreviewPanels(ctx)
	panel = panels[0]
	if panel.ResourceID == "" || previewPanelCheckboxes(panel.Body)[previewRedeployLabel] {
		t.Fatalf("deploy checkbox not accepted and reset: %+v", panel)
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	revisions, err := a.store.ListWorkflowRevisions(ctx, panel.ResourceID, 0)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("checkbox started duplicate runs: %+v %v", revisions, err)
	}
	if _, err := a.workflows.Deactivate(ctx, panel.ResourceID); err != nil {
		t.Fatal(err)
	}
	if err := a.syncWorkflowPreviewPanels(ctx); err != nil {
		t.Fatal(err)
	}
	fake.edit(panel.CommentID, "maintainer", previewRedeployLabel, true)
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	resource, err := a.store.GetWorkflowResource(ctx, panel.ResourceID)
	if err != nil || !resource.Active {
		t.Fatalf("deploy control could not resume a paused preview: %+v %v", resource, err)
	}
	revisions, err = a.store.ListWorkflowRevisions(ctx, panel.ResourceID, 0)
	if err != nil || len(revisions) != 2 {
		t.Fatalf("paused preview did not start exactly one new run: %+v %v", revisions, err)
	}
}

func TestLinkedPreviewPanelsShareActionsAndUnlink(t *testing.T) {
	ctx := context.Background()
	a, fake, template := previewPanelFixture(t)
	_, resolver, _, _, err := a.previewPanelGitHub(ctx, "github", "example/service")
	if err != nil {
		t.Fatal(err)
	}
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	event := core.IncomingEvent{Command: "/preview", Arguments: "with ui=#84", PullRequestNumber: 42, HeadSHA: strings.Repeat("a", 40), SourceCommentID: "900", TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	if err := a.syncWorkflowPreviewPanels(ctx); err != nil {
		t.Fatal(err)
	}
	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil || len(panels) != 2 {
		t.Fatalf("linked PR panel missing: %+v %v", panels, err)
	}
	var linked core.WorkflowPreviewPanel
	for _, p := range panels {
		if p.Repository == "example/ui" {
			linked = p
		}
	}
	if linked.ResourceID != target.workflowTriggers[0].ResourceID {
		t.Fatal("linked PR has its own preview")
	}
	comment := fake.edit(linked.CommentID, "maintainer", previewLiveLabel, true)
	reader, _, _, _, err := a.previewPanelGitHub(ctx, "github", "example/ui")
	if err != nil {
		t.Fatal(err)
	}
	linkedTarget := &previewPollTarget{connectionID: "github", repository: "example/ui", workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	handled, err := a.processWorkflowPreviewPanelEdit(ctx, linkedTarget, reader, comment)
	if err != nil || !handled {
		t.Fatalf("linked control failed: %v", err)
	}
	if err := a.syncWorkflowPreviewPanels(ctx); err != nil {
		t.Fatal(err)
	}
	triggers, err := a.store.ListWorkflowPreviewTriggers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, tr := range triggers {
		if tr.ClosedAt == nil {
			active++
			if !tr.LiveReload {
				t.Fatal("linked checkbox did not change canonical preview")
			}
		}
	}
	if active != 1 {
		t.Fatalf("created %d active previews", active)
	}
	event.SourceCommentID = "901"
	event.Arguments = "without ui"
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	if err := a.syncWorkflowPreviewPanels(ctx); err != nil {
		t.Fatal(err)
	}
	panels, _ = a.store.ListWorkflowPreviewPanels(ctx)
	for _, p := range panels {
		if p.Repository == "example/ui" && !strings.Contains(p.Body, "no longer linked") {
			t.Fatalf("unlink left an active mirror: %s", p.Body)
		}
	}
}

func TestPreviewCleanupArchivesOldClosedRowsAndUIHelmCleanup(t *testing.T) {
	ctx := context.Background()
	a, data, executor, _ := previewLifetimeFixture(t, "0", nil)
	if err := data.CloseWorkflowPreviewTrigger(ctx, "trigger", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.workflows.Deactivate(ctx, "resource"); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileRemovedWorkflowPreviews(ctx); err != nil {
		t.Fatal(err)
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.State != "removed" || !executor.cleaned["current"] {
		t.Fatalf("stale preview remains: %+v %v", resource, err)
	}
	// A fresh fixture tests the UI's Helm cleanup path, including all releases.
	a, data, executor, _ = previewLifetimeFixture(t, "0", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "current")
	request := httptest.NewRequest(http.MethodPost, "/api/v1/apps/current/cleanup", nil).WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	a.cleanupApp(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("UI cleanup failed: %d %s", recorder.Code, recorder.Body.String())
	}
	resource, err = data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.State != "removed" || !executor.cleaned["current"] || !executor.cleaned["older"] {
		t.Fatalf("UI cleanup left preview or releases: %+v %v", resource, err)
	}
}

func TestPreviewPanelRejectsTextChangesAndKeepsDetailsCollapsed(t *testing.T) {
	trigger := core.WorkflowPreviewTrigger{Command: "/preview", Repository: "example/service", TTL: "1d"}
	panel := core.WorkflowPreviewPanel{ID: "panel"}
	body := previewPanelBody(panel, core.WorkflowPreviewTemplate{}, &trigger, core.WorkflowResource{Name: "preview"}, previewPanelRuns{Latest: core.WorkflowRevision{ID: "revision", State: "succeeded"}, Deployed: core.WorkflowRevision{ID: "revision", State: "succeeded"}}, "https://github.example", true)
	changed := strings.Replace(body, "- [ ] "+previewTestLabel, "- [x] "+previewTestLabel, 1)
	if got := previewPanelActions(body, changed); len(got) != 1 || got[0] != "test" {
		t.Fatalf("checkbox not recognized: %v", got)
	}
	if got := previewPanelActions(body, changed+"\n/preview with service=#999"); len(got) != 0 {
		t.Fatal("arbitrary comment text became an action")
	}
	if !strings.Contains(body, "<summary>Deployment details</summary>") || strings.Contains(body, "<details open") {
		t.Fatal("deployment details are not collapsed")
	}
}

func TestPreviewPanelWebhookAndPollingShareReceipt(t *testing.T) {
	ctx := context.Background()
	a, fake, template := previewPanelFixture(t)
	if err := a.ensureAvailableWorkflowPreviewPanel(ctx, template, "example/service", 42); err != nil {
		t.Fatal(err)
	}
	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	panel := panels[0]
	fake.edit(panel.CommentID, "maintainer", previewRedeployLabel, true)
	event := core.IncomingEvent{Kind: core.EventKindPullRequestComment, Action: "edited", ProviderConnectionID: "github", Repository: "example/service", PullRequestNumber: 42, SourceCommentID: panel.CommentID}
	if err := a.processWorkflowPreviewWebhook(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := a.processWorkflowPreviewWebhook(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := a.PollPreviewsOnce(ctx); err != nil {
		t.Fatal(err)
	}
	panels, err = a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := a.store.ListWorkflowRevisions(ctx, panels[0].ResourceID, 0)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("webhook and poll duplicated an action: %+v %v", revisions, err)
	}
}

func TestPreviewCleanupFailureRemainsVisibleUntilRetry(t *testing.T) {
	ctx := context.Background()
	a, data, executor, _ := previewLifetimeFixture(t, "0", nil)
	executor.failure = fmt.Errorf("cluster unavailable")
	handled, err := a.removeWorkflowPreviewForApp(ctx, "current")
	if !handled || err == nil {
		t.Fatal("failed cleanup was accepted")
	}
	resource, err := data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.State == "removed" || resource.Active {
		t.Fatalf("failed cleanup hid the preview or left automatic updates enabled: %+v %v", resource, err)
	}
	triggers, err := data.ListWorkflowPreviewTriggers(ctx)
	if err != nil || triggers[0].ClosedAt != nil {
		t.Fatal("failed cleanup closed its trigger")
	}
	executor.failure = nil
	handled, err = a.removeWorkflowPreviewForApp(ctx, "current")
	if !handled || err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
	resource, err = data.GetWorkflowResource(ctx, "resource")
	if err != nil || resource.State != "removed" {
		t.Fatalf("cleanup retry left stale row: %+v %v", resource, err)
	}
}

func TestPreviewPanelLeasePreventsConcurrentControllers(t *testing.T) {
	ctx := context.Background()
	_, data, _, _ := previewLifetimeFixture(t, "0", nil)
	now := time.Now().UTC()
	claimed, err := data.ClaimWorkflowPreviewPanelLease(ctx, "first", now)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	claimed, err = data.ClaimWorkflowPreviewPanelLease(ctx, "second", now)
	if err != nil || claimed {
		t.Fatal("another controller acquired the active panel lease")
	}
	if err := data.ReleaseWorkflowPreviewPanelLease(ctx, "second"); err != nil {
		t.Fatal(err)
	}
	claimed, err = data.ClaimWorkflowPreviewPanelLease(ctx, "third", now)
	if err != nil || claimed {
		t.Fatal("a different owner released the lease")
	}
	claimed, err = data.ClaimWorkflowPreviewPanelLease(ctx, "second", now.Add(11*time.Minute))
	if err != nil || !claimed {
		t.Fatal("restart could not recover the expired lease")
	}
}

func TestClosingLinkedPRKeepsPreviewUntilPrimaryAlsoCloses(t *testing.T) {
	ctx := context.Background()
	a, fake, template := previewPanelFixture(t)
	_, resolver, _, _, err := a.previewPanelGitHub(ctx, "github", "example/service")
	if err != nil {
		t.Fatal(err)
	}
	target := &previewPollTarget{connectionID: "github", repository: "example/service", workflowTemplates: []core.WorkflowPreviewTemplate{template}}
	event := core.IncomingEvent{Command: "/preview", Arguments: "with ui=#84", PullRequestNumber: 42, HeadSHA: strings.Repeat("a", 40), SourceCommentID: "900", TrustedActor: true}
	if err := a.processWorkflowPreviewComment(ctx, target, event, resolver); err != nil {
		t.Fatal(err)
	}
	if err := a.syncWorkflowPreviewPanels(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.closed = map[string]bool{"example/ui": true}
	fake.mu.Unlock()
	closure := core.IncomingEvent{Kind: core.EventKindPullRequest, Action: "closed", ProviderConnectionID: "github", Repository: "example/ui", PullRequestNumber: 84}
	if err := a.processWorkflowPreviewWebhook(ctx, closure); err != nil {
		t.Fatal(err)
	}
	resource, err := a.store.GetWorkflowResource(ctx, target.workflowTriggers[0].ResourceID)
	if err != nil || !resource.Active {
		t.Fatal("closing the linked PR removed an open primary preview")
	}
	fake.mu.Lock()
	fake.closed["example/service"] = true
	fake.mu.Unlock()
	closure.Repository = "example/service"
	closure.PullRequestNumber = 42
	if err := a.processWorkflowPreviewWebhook(ctx, closure); err != nil {
		t.Fatal(err)
	}
	resource, err = a.store.GetWorkflowResource(ctx, resource.ID)
	if err != nil || resource.State != "removed" {
		t.Fatalf("closed preview remains visible: %+v %v", resource, err)
	}
	panels, err := a.store.ListWorkflowPreviewPanels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, panel := range panels {
		if panel.ResourceID == resource.ID && (!strings.Contains(panel.Body, "Removed.") || strings.Contains(panel.Body, "[Open preview]")) {
			t.Fatalf("closed panel still advertises deployment: %s", panel.Body)
		}
	}
}

func TestPreviewPanelShowsDeployedCommitWhileNextBuildRuns(t *testing.T) {
	runs := previewPanelRuns{
		Latest:   core.WorkflowRevision{ID: "next", State: "running", Sources: map[string]core.WorkflowSourceRevision{"service": {Repository: "example/service", CommitSHA: "next-commit"}}},
		Deployed: core.WorkflowRevision{ID: "previous", State: "succeeded", Sources: map[string]core.WorkflowSourceRevision{"service": {Repository: "example/service", CommitSHA: "deployed-commit"}}},
	}
	body := previewPanelBody(core.WorkflowPreviewPanel{ID: "panel"}, core.WorkflowPreviewTemplate{}, &core.WorkflowPreviewTrigger{Command: "/preview", PreviewURL: "https://preview.example/42"}, core.WorkflowResource{Name: "preview-42"}, runs, "https://github.example", true)
	for _, want := range []string{"Status: running", "[Open preview](https://preview.example/42)", "<summary>Deployment details</summary>", "<summary>Current run</summary>", "deployed-commit", "next-commit"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
}

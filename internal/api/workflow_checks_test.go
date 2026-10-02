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
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/githubapp"
)

type checkFixture struct {
	a                               *API
	revision                        core.WorkflowRevision
	report                          core.WorkflowCheckReport
	runs                            []githubapp.CheckRun
	writes                          []githubapp.CheckRunUpdate
	posts, patches                  int
	forbidden, lost, missing, spoof bool
}

func newCheckFixture(t *testing.T) *checkFixture {
	t.Helper()
	ctx := context.Background()
	f := &checkFixture{a: serviceTestAPI(t)}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/installation"):
			fmt.Fprint(w, `{"id":73,"app_id":42}`)
		case r.URL.Path == "/app/installations/73/access_tokens":
			json.NewEncoder(w).Encode(map[string]any{"token": "fixture-installation-secret", "expires_at": time.Now().Add(time.Hour)})
		case strings.Contains(r.URL.Path, "/commits/"):
			if !strings.Contains(r.URL.Path, f.report.CommitSHA) || r.URL.Query().Get("app_id") != "42" || r.URL.Query().Get("check_name") != f.report.Name {
				t.Errorf("not pinned: %s", r.URL)
			}
			runs := f.runs
			if f.missing {
				runs = nil
			}
			if f.spoof {
				foreign := githubapp.CheckRun{ID: 900, Name: f.report.Name, SHA: f.report.CommitSHA, ExternalID: f.report.ExternalID}
				foreign.App.ID = 999
				runs = append([]githubapp.CheckRun{foreign}, runs...)
			}
			json.NewEncoder(w).Encode(map[string]any{"check_runs": runs})
		case strings.Contains(r.URL.Path, "/check-runs"):
			if r.Method == http.MethodGet {
				if len(f.runs) == 0 {
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(f.runs[0])
				return
			}
			if r.Header.Get("Authorization") != "Bearer fixture-installation-secret" {
				t.Error("not scoped installation token")
			}
			if r.Method == http.MethodPost {
				f.posts++
			} else {
				f.patches++
			}
			var p githubapp.CheckRunUpdate
			must(json.NewDecoder(r.Body).Decode(&p))
			f.writes = append(f.writes, p)
			if f.forbidden {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"message":"secret-upstream-token"}`)
				return
			}
			run := githubapp.CheckRun{ID: 101, Name: p.Name, SHA: f.report.CommitSHA, ExternalID: p.ExternalID, HTMLURL: "https://github.example.test/example/repo/runs/101"}
			run.App.ID = 42
			if r.Method == http.MethodPost {
				if p.SHA != f.report.CommitSHA {
					t.Error("created on wrong commit")
				}
				f.runs = append(f.runs, run)
			}
			if f.lost {
				f.lost = false
				w.WriteHeader(502)
				fmt.Fprint(w, `{"message":"secret-lost-response"}`)
				return
			}
			json.NewEncoder(w).Encode(run)
		default:
			t.Errorf("unexpected reporting request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	manager := githubapp.New(f.a.store, f.a.eventConfig.Vault)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(err)
	encrypted, webhook, err := manager.EncryptCredentials("check-app", string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "0123456789abcdef")
	must(err)
	now := time.Now().UTC()
	must(f.a.store.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "check-app", Name: "Checks", AppID: 42, InstallationID: 73, APIURL: server.URL, WebURL: "https://github.example.test", EncryptedPrivateKey: encrypted, EncryptedWebhookSecret: webhook, State: "ready", CreatedAt: now, UpdatedAt: now}))
	f.a.eventConfig.GitHubApps = manager
	f.a.auth.PublicURL = "https://dispatch.example.test"
	projects, err := f.a.store.ListProjects(ctx)
	must(err)
	must(f.a.store.CreateConfigSource(ctx, core.ConfigSource{ID: "check-source", GitHubAppID: "check-app", ProjectID: projects[0].ID, Name: "Checks", Repository: "example/repo", Active: true, CreatedAt: now, UpdatedAt: now}))
	must(f.a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: "check-resource", ConfigSourceID: "check-source", Kind: "Application", Name: "application", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	f.report = core.WorkflowCheckReport{ID: "report-1", RevisionID: "check-run", ResourceID: "check-resource", ProjectID: projects[0].ID, GitHubAppID: "check-app", AppID: 42, APIURL: server.URL, Repository: "example/repo", CommitSHA: strings.Repeat("a", 40), Name: "Dispatch/deployment/application", Kind: "deployment", ExternalID: "dispatch-check:report-1", PreviewURL: "https://preview.example.test"}
	f.revision = core.WorkflowRevision{ID: "check-run", ResourceID: "check-resource", State: "queued", CreatedAt: now, Checks: []core.WorkflowCheckReport{f.report}}
	must(f.a.store.CreateWorkflowRevision(ctx, f.revision))
	return f
}
func (f *checkFixture) step(t *testing.T) core.WorkflowCheckReport {
	t.Helper()
	r, err := f.a.store.ClaimWorkflowCheck(context.Background(), time.Now().Add(time.Hour), 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.a.reportWorkflowCheck(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	items, err := f.a.store.ListWorkflowChecks(context.Background(), f.revision.ID)
	if err != nil || len(items) != 1 {
		t.Fatalf("checks %v %v", items, err)
	}
	return items[0]
}
func TestWorkflowChecksRecoverResponseLossWithoutDuplicate(t *testing.T) {
	f := newCheckFixture(t)
	f.lost = true
	f.spoof = true
	r := f.step(t)
	if r.State != "retrying" || r.CreateState != "posting" || r.CheckID != 0 || f.posts != 1 {
		t.Fatalf("lost response: %+v posts%d", r, f.posts)
	}
	// A lagging list or another App's spoof never authorizes a second create.
	f.missing = true
	r = f.step(t)
	if f.posts != 1 || r.CheckID != 0 {
		t.Fatalf("duplicate after unknown: %+v", r)
	}
	f.missing = false
	r = f.step(t)
	if f.posts != 1 || r.CheckID != 101 || r.Status != "queued" || r.Error != "" {
		t.Fatalf("reconcile: %+v", r)
	}
	f.revision.State = "running"
	now := time.Now().UTC()
	f.revision.StartedAt = &now
	if err := f.a.store.UpdateWorkflowRevision(context.Background(), f.revision); err != nil {
		t.Fatal(err)
	}
	r = f.step(t)
	if r.Status != "in_progress" || f.posts != 1 {
		t.Fatalf("running: %+v", r)
	}
	// The input head may have moved; only the captured SHA is ever used.
	f.revision.State = "succeeded"
	f.revision.FinishedAt = &now
	if err := f.a.store.UpdateWorkflowRevision(context.Background(), f.revision); err != nil {
		t.Fatal(err)
	}
	r = f.step(t)
	if !r.Complete || r.Conclusion != "success" || f.posts != 1 || f.patches != 3 {
		t.Fatalf("terminal: %+v posts%d patches%d", r, f.posts, f.patches)
	}
	if f.writes[len(f.writes)-1].DetailsURL != "https://dispatch.example.test/events?run=check-run" {
		t.Fatal("missing run link")
	}
}
func TestWorkflowChecksPermissionFailureDoesNotChangeVerdict(t *testing.T) {
	f := newCheckFixture(t)
	f.forbidden = true
	f.revision.State = "failed"
	f.revision.Error = "SECRET_JOB_OUTPUT"
	if err := f.a.store.UpdateWorkflowRevision(context.Background(), f.revision); err != nil {
		t.Fatal(err)
	}
	r := f.step(t)
	if !strings.Contains(r.Error, "Checks: write") || r.CreateState != "" || r.Complete {
		t.Fatalf("permission evidence: %+v", r)
	}
	saved, err := f.a.store.GetWorkflowRevision(context.Background(), f.revision.ID)
	if err != nil || saved.State != "failed" || saved.Error != f.revision.Error {
		t.Fatalf("reporting changed execution: %+v %v", saved, err)
	}
	f.forbidden = false
	r = f.step(t)
	if !r.Complete || r.Conclusion != "failure" || f.posts != 2 {
		t.Fatalf("permission repair: %+v", r)
	}
	value, _ := json.Marshal(struct {
		Report core.WorkflowCheckReport
		Writes []githubapp.CheckRunUpdate
	}{r, f.writes})
	if strings.Contains(string(value), "SECRET_JOB_OUTPUT") || strings.Contains(string(value), "secret-upstream-token") {
		t.Fatal("report leaked private evidence")
	}
}
func TestWorkflowCheckStateMappingAndSanitizedLinks(t *testing.T) {
	a := &API{}
	for _, test := range []struct{ state, status, conclusion string }{{"queued", "queued", ""}, {"running", "in_progress", ""}, {"awaiting_approval", "in_progress", ""}, {"succeeded", "completed", "success"}, {"failed", "completed", "failure"}, {"cancelled", "completed", "cancelled"}} {
		p, err := a.workflowCheckPayload(context.Background(), core.WorkflowCheckReport{Kind: "qa", PreviewURL: "https://user:private@example.test/path?token=secret"}, core.WorkflowRevision{State: test.state, Error: "private-log"})
		if err != nil || p.Status != test.status || p.Conclusion != test.conclusion || p.DetailsURL != "" || strings.Contains(p.Output.Summary, "private") {
			t.Fatalf("mapping %s: %+v %v", test.state, p, err)
		}
	}
	p, err := a.workflowCheckPayload(context.Background(), core.WorkflowCheckReport{}, core.WorkflowRevision{State: "failed", Error: core.WorkflowInterruptedMessage})
	if err != nil || p.Conclusion != "action_required" {
		t.Fatalf("interrupted result: %+v %v", p, err)
	}
}
func TestWorkflowChecksHealthUsesRecordedChildAndSkipsUnrun(t *testing.T) {
	f := newCheckFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// Pipeline checks have their own resource identity, referenced by the saved stage.
	child := core.WorkflowRevision{ID: "health-child", ResourceID: "check-resource", State: "failed", CreatedAt: now}
	if err := f.a.store.CreateWorkflowRevision(ctx, child); err != nil {
		t.Fatal(err)
	}
	stage := core.WorkflowStageRun{ID: "stage", RevisionID: f.revision.ID, StageName: "preview", State: "failed", CheckRuns: map[string]string{"ready": child.ID}, CreatedAt: now}
	if err := f.a.store.CreateWorkflowStageRun(ctx, stage); err != nil {
		t.Fatal(err)
	}
	r := f.report
	r.Kind = "health"
	r.Stage = "preview"
	r.Check = "ready"
	p, err := f.a.workflowCheckPayload(ctx, r, f.revision)
	if err != nil || p.Conclusion != "failure" {
		t.Fatalf("recorded health %+v %v", p, err)
	}
	r.Check = "not-run"
	f.revision.State = "succeeded"
	p, err = f.a.workflowCheckPayload(ctx, r, f.revision)
	if err != nil || p.Conclusion != "skipped" {
		t.Fatalf("unrun check falsely passed %+v %v", p, err)
	}
	if f.posts != 0 || f.patches != 0 {
		t.Fatal("reading existing health launched reporting or execution")
	}
}

func TestWorkflowChecksRejectChangedAppAndForeignSavedCheck(t *testing.T) {
	f := newCheckFixture(t)
	ctx := context.Background()
	first := f.step(t)
	if first.CheckID != 101 {
		t.Fatal("missing initial check")
	}
	f.revision.State = "running"
	if err := f.a.store.UpdateWorkflowRevision(ctx, f.revision); err != nil {
		t.Fatal(err)
	}
	f.runs[0].App.ID = 900
	r := f.step(t)
	if r.State != "retrying" || f.patches != 0 || !strings.Contains(r.Error, "no longer matches") {
		t.Fatalf("foreign check updated: %+v", r)
	}
	f.runs[0].App.ID = 42
	connection, err := f.a.store.GetGitHubApp(ctx, "check-app")
	if err != nil {
		t.Fatal(err)
	}
	connection.AppID = 900
	if err := f.a.store.UpdateGitHubApp(ctx, connection); err != nil {
		t.Fatal(err)
	}
	r = f.step(t)
	if r.State != "retrying" || f.patches != 0 || !strings.Contains(r.Error, "identity or API host changed") {
		t.Fatalf("reconfigured App got old report: %+v", r)
	}
}

func TestWorkflowChecksRetryUpdateOnSameSavedID(t *testing.T) {
	f := newCheckFixture(t)
	first := f.step(t)
	f.revision.State = "cancelled"
	if err := f.a.store.UpdateWorkflowRevision(context.Background(), f.revision); err != nil {
		t.Fatal(err)
	}
	f.lost = true
	pending := f.step(t)
	if pending.Complete || pending.CheckID != first.CheckID || pending.State != "retrying" || f.posts != 1 {
		t.Fatalf("lost update reset identity: %+v", pending)
	}
	done := f.step(t)
	if !done.Complete || done.Conclusion != "cancelled" || done.CheckID != first.CheckID || f.posts != 1 || f.patches != 2 {
		t.Fatalf("terminal update not recovered: %+v", done)
	}
}

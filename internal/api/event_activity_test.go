package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/oklog/ulid/v2"
)

func TestEventsIncludePollingTemplatesAndScopeHistoryBeforePagination(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	projects, _ := a.store.ListProjects(ctx)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	visible := projects[0].ID
	hidden := "hidden-project"
	must(a.store.CreateProject(ctx, core.Project{ID: hidden, Name: "Private", CreatedAt: now}))
	must(a.store.CreateSecret(ctx, core.Secret{ID: "credential", Name: "Credential", Type: core.SecretTypeText, CreatedAt: now, UpdatedAt: now}))
	must(a.store.CreateGitHubApp(ctx, core.GitHubAppConnection{ID: "github", Name: "GitHub", State: "ready", CreatedAt: now, UpdatedAt: now}))
	source := core.ConfigSource{ID: "poll-config", CredentialSecretID: "credential", ProjectID: visible, Name: "Deployment repository", Repository: "team/deployments", Branch: "main", SyncMode: core.ConfigSyncPoll, PollIntervalSeconds: 60, Active: true, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateConfigSource(ctx, source))
	must(a.store.CreateWorkflowPreviewTemplate(ctx, core.WorkflowPreviewTemplate{ID: "preview-template", GitHubAppID: "github", ConfigSourceID: source.ID, Name: "PR previews", Repository: "team/service", WatchRepositories: []string{"team/service", "team/ui"}, Command: "/preview", Active: true, CreatedAt: now, UpdatedAt: now}))
	check := core.EventActivity{ID: ulid.Make().String(), ProjectID: visible, RuleID: previewCheckKey("github", "team/ui"), Name: "team/ui", Transport: "poll", Kind: "preview_check", State: "failed", Message: "GitHub returned 503", Repository: "team/ui", CreatedAt: now, Check: true}
	must(a.store.SavePollCheck(ctx, check.RuleID, check))
	for i := 0; i < 55; i++ {
		item := core.EventActivity{ID: ulid.Make().String(), ProjectID: visible, RuleID: "template:preview-template", Name: "PR previews", Transport: "poll", Kind: "pull_request_comment", Repository: "team/ui", State: "processed", CreatedAt: now}
		must(a.store.SaveEventActivity(ctx, fmt.Sprint(i), item))
	}
	// More recent inaccessible events must not consume the user's page or its count.
	for i := 0; i < 55; i++ {
		item := core.EventActivity{ID: ulid.Make().String(), ProjectID: hidden, RuleID: "private", Name: "Private template", Transport: "webhook", Kind: "push", State: "processed", CreatedAt: now}
		must(a.store.SaveEventActivity(ctx, fmt.Sprint(i), item))
	}
	user := core.User{ID: "viewer", Username: "viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateUser(ctx, user))
	must(a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "viewer-project", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: visible, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}))
	token, err := a.createSession(ctx, user.ID, identityForUser(user))
	must(err)
	get := func(path string, auth bool, want int) []byte {
		t.Helper()
		rr := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		a.ServeHTTP(rr, r)
		if rr.Code != want {
			t.Fatalf("%s: %d %s", path, rr.Code, rr.Body.String())
		}
		return rr.Body.Bytes()
	}
	var rules []eventRule
	must(json.Unmarshal(get("/api/v1/events/rules", true, 200), &rules))
	var template *eventRule
	for i := range rules {
		if rules[i].ID == "template:preview-template" {
			template = &rules[i]
		}
	}
	if template == nil || len(template.Repositories) != 2 || template.Mode != "poll" || template.Check == nil || template.Check.State != "failed" {
		t.Fatalf("preview polling rule or failure missing: %+v", rules)
	}
	raw := get("/api/v1/events/activity?transport=poll", true, 200)
	var page struct {
		Items []core.EventActivity `json:"items"`
		Next  string               `json:"next"`
		Total int                  `json:"total"`
	}
	must(json.Unmarshal(raw, &page))
	if len(page.Items) != 50 || page.Next == "" || page.Total != 56 || strings.Contains(string(raw), "Private") {
		t.Fatalf("scoped first page: %s", raw)
	}
	must(json.Unmarshal(get("/api/v1/events/activity?transport=poll&before="+page.Next, true, 200), &page))
	if len(page.Items) != 6 || page.Total != 56 {
		t.Fatalf("scoped next page: %+v", page)
	}
	get("/api/v1/events/activity?transport=invalid", true, 400)
	for _, path := range []string{"/api/v1/events/activity", "/api/v1/events/rules"} {
		get(path, false, 401)
	}
}

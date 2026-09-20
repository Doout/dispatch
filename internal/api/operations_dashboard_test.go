package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestOperationsDashboardScopeSearchAndCursor(t *testing.T) {
	a := serviceTestAPI(t)
	enableOperationsForTest(t, a)
	ctx := context.Background()
	data := a.store.(operationsStore)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"dashboard-public", "dashboard-private"} {
		must(a.store.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}))
	}
	servers, err := a.store.ListServers(ctx)
	must(err)
	for i := 0; i < 105; i++ {
		must(a.store.CreateApp(ctx, core.App{ID: fmt.Sprintf("dashboard-app-%03d", i), ProjectID: "dashboard-public", Name: fmt.Sprintf("Application %03d", i), ServerID: servers[0].ID, BuildType: "dockerfile", Generated: i == 104, CreatedAt: now}))
	}
	must(a.store.CreateApp(ctx, core.App{ID: "private-owner-app", ProjectID: "dashboard-private", Name: "Private application", ServerID: servers[0].ID, BuildType: "dockerfile", CreatedAt: now}))
	user := core.User{ID: "dashboard-viewer", Username: "dashboard-viewer", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateUser(ctx, user))
	must(a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "dashboard-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: "dashboard-public", Role: "viewer", CreatedAt: now, UpdatedAt: now}))
	for _, e := range []core.AuditEvent{{ID: "dashboard-event", ProjectID: "dashboard-public", ActorName: "Audit actor", Outcome: "rejected", CreatedAt: now}, {ID: "dashboard-old", ProjectID: "dashboard-public", ActorName: "Old actor", Outcome: "rejected", CreatedAt: now.Add(-25 * time.Hour)}, {ID: "dashboard-private-event", ProjectID: "dashboard-private", ActorName: "Private actor", Outcome: "rejected", CreatedAt: now}, {ID: "dashboard-controller-event", ActorName: "Controller actor", Outcome: "succeeded", CreatedAt: now}} {
		e.Action = "PUT /api/v1/apps/{id}/owner"
		must(data.AppendAuditEvent(ctx, e))
	}
	must(data.SaveBackupRecord(ctx, core.BackupRecord{ID: "dashboard-backup", Engine: "sqlite", State: "ready", Bytes: 123, CreatedAt: now, Message: "Backup created"}))
	request := func(path string, want int) []byte {
		t.Helper()
		r := tokenRequest("GET", path, nil)
		r.Header.Set("Impersonate-User", user.ID)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if want == 200 && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("sensitive operational response is cacheable")
		}
		return w.Body.Bytes()
	}
	var summary core.OperationsSummary
	must(json.Unmarshal(request("/api/v1/operations/summary", 200), &summary))
	if summary.Audit.Total != 1 || summary.Audit.Rejected != 1 || len(summary.Audit.Recent) != 1 || summary.Ownership.Total != 105 || summary.Ownership.Unassigned != 105 || summary.Backups != nil {
		t.Fatal("viewer summary widened permission scope", summary)
	}
	query := url.Values{"since": {summary.Audit.Since.Format(time.RFC3339Nano)}, "until": {summary.ObservedAt.Format(time.RFC3339Nano)}, "outcome": {"rejected"}, "q": {"Audit actor"}}
	var events []core.AuditEvent
	must(json.Unmarshal(request("/api/v1/audit?"+query.Encode(), 200), &events))
	if int64(len(events)) != summary.Audit.Rejected {
		t.Fatal("summary rejection drilldown differs from same audit window")
	}
	var page struct {
		Items []core.OwnershipItem `json:"items"`
		Next  string               `json:"next"`
	}
	must(json.Unmarshal(request("/api/v1/operations/ownership?q=Application&unassigned=true", 200), &page))
	if len(page.Items) != 100 || page.Next == "" {
		t.Fatal("ownership first page not bounded")
	}
	seen := map[string]bool{}
	for _, v := range page.Items {
		seen[v.AppID] = true
		if v.ProjectID != "dashboard-public" {
			t.Fatal("private ownership leaked")
		}
	}
	next := page.Next
	page.Items = nil
	page.Next = ""
	must(json.Unmarshal(request("/api/v1/operations/ownership?q=Application&unassigned=true&before="+next, 200), &page))
	if len(page.Items) != 5 || page.Next != "" {
		t.Fatal("ownership final page wrong")
	}
	for _, v := range page.Items {
		if seen[v.AppID] {
			t.Fatal("cursor duplicated ownership item")
		}
	}
	must(data.SaveApplicationOwner(ctx, core.ApplicationOwner{AppID: "dashboard-app-000", PrincipalType: "user", PrincipalID: user.ID, UpdatedAt: now}))
	var assigned struct {
		Items []core.OwnershipItem `json:"items"`
		Next  string               `json:"next"`
	}
	must(json.Unmarshal(request("/api/v1/operations/ownership?unassigned=false", 200), &assigned))
	if len(assigned.Items) != 1 || assigned.Items[0].AppID != "dashboard-app-000" || assigned.Items[0].Owner == nil || assigned.Next != "" {
		t.Fatal("unassigned=false did not filter assigned applications")
	}
	request("/api/v1/operations/summary?projectId=dashboard-private", 403)
	request("/api/v1/operations/ownership?projectId=dashboard-private", 403)
	for _, path := range []string{"/api/v1/operations/summary", "/api/v1/operations/ownership"} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal("unauthenticated operations access", path, w.Code)
		}
	}
	for _, path := range []string{"/api/v1/audit?outcome=failed", "/api/v1/audit?since=bad", "/api/v1/audit?until=", "/api/v1/audit?since=2030-01-02T00:00:00Z&until=2030-01-01T00:00:00Z", "/api/v1/operations/ownership?unassigned=maybe", "/api/v1/operations/ownership?before=invalid", "/api/v1/audit?q=" + strings.Repeat("a", 201)} {
		request(path, 400)
	}
	var owned core.OperationsSummary
	must(json.Unmarshal(serviceRequestTest(t, a, "GET", "/api/v1/operations/summary?projectId=dashboard-public", nil, 200), &owned))
	if owned.Backups == nil || owned.Backups.Recorded != 1 || owned.Backups.Latest.ID != "dashboard-backup" {
		t.Fatal("owner backup metadata lost controller scope")
	}
	must(a.store.DeleteRoleAssignment(ctx, "dashboard-grant"))
	summary = core.OperationsSummary{}
	must(json.Unmarshal(request("/api/v1/operations/summary", 200), &summary))
	if summary.Audit.Total != 0 || summary.Ownership.Total != 0 || summary.Backups != nil {
		t.Fatal("revoked grant remained effective")
	}
	request("/api/v1/operations/ownership?projectId=dashboard-public", 403)
}

func TestRetentionReviewRejectsChangedPolicyBeforePreviewOrApply(t *testing.T) {
	a := serviceTestAPI(t)
	enableOperationsForTest(t, a)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	project := projects[0].ID
	data := a.store.(operationsStore)
	original, err := data.GetRetentionPolicy(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/projects/" + project + "/retention"
	serviceRequestTest(t, a, "POST", base+"/preview", map[string]any{"expectedPolicy": original}, 200)
	changed := original
	changed.LogDays = 1
	if err = data.SaveRetentionPolicy(ctx, changed); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"confirm": project, "expectedPolicy": original}
	serviceRequestTest(t, a, "POST", base+"/preview", body, 409)
	serviceRequestTest(t, a, "POST", base+"/apply", body, 409)
	current, err := data.GetRetentionPolicy(ctx, project)
	if err != nil || current != changed {
		t.Fatal("reviewed apply replaced newer policy")
	}
	serviceRequestTest(t, a, "POST", base+"/apply", map[string]any{"confirm": project, "expectedPolicy": changed}, 200)
	// Old callers keep empty preview bodies and confirm-only applies.
	r := tokenRequest("POST", base+"/preview", bytes.NewReader(nil))
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("empty preview body stopped working", w.Code, w.Body.String())
	}
	serviceRequestTest(t, a, "POST", base+"/apply", map[string]any{"confirm": project}, 200)
	wrong := original
	wrong.ProjectID = "another-project"
	serviceRequestTest(t, a, "POST", base+"/apply", map[string]any{"confirm": project, "expectedPolicy": wrong}, 400)
	for _, invalid := range []core.RetentionPolicy{
		{ProjectID: project},
		{ProjectID: project, LogDays: -1, RunDays: 90, KeepRuns: 20},
		{ProjectID: project, LogDays: 30, RunDays: 0, KeepRuns: 20},
		{ProjectID: project, LogDays: 30, RunDays: 90, KeepRuns: 4},
		{ProjectID: project, LogDays: 36501, RunDays: 90, KeepRuns: 20},
		{ProjectID: project, LogDays: 30, RunDays: 36501, KeepRuns: 20},
		{ProjectID: project, LogDays: 30, RunDays: 90, KeepRuns: 10001},
	} {
		for _, action := range []string{"preview", "apply"} {
			serviceRequestTest(t, a, "POST", base+"/"+action, map[string]any{"confirm": project, "expectedPolicy": invalid}, 400)
		}
	}
}

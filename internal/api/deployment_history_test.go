package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func TestDeploymentComparisonUsesSavedInputsAndRedacts(t *testing.T) {
	snapshot := func(values map[string]any) core.DeploymentSnapshot {
		return core.DeploymentSnapshot{TargetID: "cluster", Values: values}
	}
	from := core.Deployment{ID: "old", CommitSHA: "old-sha", Snapshot: snapshot(map[string]any{"replicas": float64(2), "removed": true, "database": map[string]any{"host": "old-db", "password": "old-private"}, "null": nil, "password": "old-private", "env": []any{map[string]any{"name": "AUTH", "value": "old-private"}}, "endpoint": "postgres://admin:old-private@db/app", "nested": map[string]any{"image": "api:v1"}})}
	to := core.Deployment{ID: "new", CommitSHA: "new-sha", Snapshot: snapshot(map[string]any{"replicas": float64(3), "added": true, "database": map[string]any{"host": "new-db", "password": "new-private"}, "null": nil, "password": "new-private", "env": []any{map[string]any{"name": "AUTH", "value": "new-private"}}, "endpoint": "postgres://admin:new-private@db/app", "nested": map[string]any{"image": "api:v2"}})}
	result := compareDeploymentSnapshots(from, to)
	if !result.Available || len(result.Changes) != 6 || result.Hidden != 4 {
		t.Fatalf("unexpected comparison: %+v", result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), "private") {
		t.Fatal("credentials exposed")
	}
	kinds := map[string]string{}
	for _, change := range result.Changes {
		kinds[change.Path] = change.Kind
	}
	if kinds["/values/added"] != "added" || kinds["/values/removed"] != "removed" || kinds["/values/nested/image"] != "changed" {
		t.Fatal(kinds)
	}
	from.Snapshot = core.DeploymentSnapshot{}
	if result = compareDeploymentSnapshots(from, to); result.Available || len(result.Changes) != 0 {
		t.Fatal("invented historical values")
	}
}

func TestApplicationHistoryPaginationAndComparisonIsolation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	now := time.Now().UTC().Truncate(time.Second)
	baselineDocument := `[{"apiVersion":"v1","kind":"Secret","metadata":{"name":"credentials","namespace":"default","uid":"retained-secret-uid"},"data":{"password":"private-baseline-value"}}]`
	for i := 0; i < 55; i++ {
		d := core.Deployment{ID: fmt.Sprintf("history-%03d", i), AppID: app.ID, CommitSHA: fmt.Sprintf("sha-%d", i), CreatedAt: now, State: core.DeploymentSucceeded, Snapshot: core.DeploymentSnapshot{TargetID: app.ServerID, Values: map[string]any{"replicas": i}}}
		if i == 25 {
			d.State = core.DeploymentFailed
		}
		if err := a.store.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		ciphertext, err := a.eventConfig.Vault.Encrypt("deployment-drift:"+d.ID, []byte(baselineDocument))
		if err != nil {
			t.Fatal(err)
		}
		if i == 10 {
			ciphertext = "unreadable-baseline"
		}
		if err := a.store.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: d.ID, AppID: app.ID, ServerID: app.ServerID, Namespace: "default", Release: "example", Ciphertext: ciphertext}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: "history-054", Level: "info", Message: "original deployment log", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	beforeBaseline, err := a.store.GetDriftBaseline(ctx, "history-054")
	if err != nil {
		t.Fatal(err)
	}
	beforeHistory, err := a.store.ListApplicationHistory(ctx, app.ID, "", 101)
	if err != nil {
		t.Fatal(err)
	}
	// Listing saved evidence needs the vault, not a configured live drift service.
	a.drift = nil
	raw := serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/deployment-history", nil, 200)
	var page struct {
		Items   []core.Deployment `json:"items"`
		Next    string            `json:"next"`
		Repeats map[string]string `json:"repeats"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 50 || page.Items[0].ID != "history-054" || page.Next != "history-005" {
		t.Fatalf("bad first page: %d %s", len(page.Items), page.Next)
	}
	if page.Repeats["history-053"] != "history-054" || page.Repeats["history-025"] != "" || page.Repeats["history-024"] != "" || page.Repeats["history-010"] != "" || page.Repeats["history-009"] != "" {
		t.Fatal("history links ignored failure or unavailable-evidence barriers", page.Repeats)
	}
	if strings.Contains(string(raw), "private-baseline-value") || strings.Contains(string(raw), "password") || strings.Contains(string(raw), "ciphertext") || strings.Contains(string(raw), "retained-secret-uid") {
		t.Fatal("history exposed private baseline contents")
	}
	raw = serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/deployment-history?before="+page.Next, nil, 200)
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) < 5 || page.Items[0].ID != "history-004" || page.Items[4].ID != "history-000" {
		t.Fatal("cursor skipped or repeated runs")
	}
	if page.Repeats["history-004"] != "history-005" {
		t.Fatal("history repeat chain lost at pagination boundary", page.Repeats)
	}
	afterBaseline, err := a.store.GetDriftBaseline(ctx, "history-054")
	if err != nil || afterBaseline != beforeBaseline {
		t.Fatal("history read changed saved baseline", err)
	}
	afterHistory, err := a.store.ListApplicationHistory(ctx, app.ID, "", 101)
	if err != nil || !reflect.DeepEqual(afterHistory, beforeHistory) {
		t.Fatal("history read changed deployment records", err)
	}
	logs, err := a.store.ListDeploymentLogs(ctx, "history-054", 0)
	if err != nil || len(logs) != 1 || logs[0].Message != "original deployment log" {
		t.Fatal("history read changed original logs", err)
	}
	other := app
	other.ID = "other-history-app"
	other.Name = "other-history-app"
	if err := a.store.CreateApp(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateDeployment(ctx, core.Deployment{ID: "other-history-deployment", AppID: other.ID, CreatedAt: now, State: core.DeploymentSucceeded}); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "GET", "/api/v1/deployments/history-054/compare?from=other-history-deployment", nil, 404)
	serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/deployment-history?before=other-history-deployment", nil, 404)
	serviceRequestTest(t, a, "GET", "/api/v1/deployments/history-054/compare?from=history-000", nil, 200)
	// Project view permission guards the new application endpoint.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", app.ID)
	req = req.WithContext(context.WithValue(withIdentity(req.Context(), core.Identity{ID: "unassigned-member", SystemRole: "member"}), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	a.appPermission(core.PermissionProjectView)(http.HandlerFunc(a.applicationDeploymentHistory)).ServeHTTP(response, req)
	if response.Code != 403 {
		t.Fatalf("unassigned member can read history: %d", response.Code)
	}
}

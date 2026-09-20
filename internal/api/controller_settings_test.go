package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func enableOperationsForTest(t *testing.T, a *API) {
	t.Helper()
	if err := a.store.SaveControllerSettings(context.Background(), core.ControllerSettings{OperationsEnabled: true}); err != nil {
		t.Fatal(err)
	}
}

func TestControllerSettingsDefaultOffAndOperationsGates(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	settings := serviceRequestTest(t, a, "GET", "/api/v1/settings", nil, 200)
	if string(bytes.TrimSpace(settings)) != `{"operationsEnabled":false}` {
		t.Fatal("Operations did not default to off", string(settings))
	}
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal(err)
	}
	app := apps[0]
	retention := "/api/v1/projects/" + app.ProjectID + "/retention"
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/audit"},
		{"GET", "/api/v1/operations/summary"},
		{"GET", "/api/v1/operations/ownership"},
		{"GET", "/api/v1/operations/backups"},
		{"POST", "/api/v1/operations/backups"},
		{"POST", "/api/v1/operations/backups/missing/verify"},
		{"GET", retention}, {"PUT", retention},
		{"POST", retention + "/preview"}, {"POST", retention + "/apply"},
		{"GET", "/api/v1/projects/" + app.ProjectID + "/owner-candidates"},
		{"PUT", "/api/v1/apps/" + app.ID + "/owner"},
		{"GET", "/api/v1/identity-team-mappings"},
		{"POST", "/api/v1/identity-team-mappings"},
		{"DELETE", "/api/v1/identity-team-mappings/missing"},
	}
	checkDisabled := func() {
		t.Helper()
		for _, route := range routes {
			body := serviceRequestTest(t, a, route.method, route.path, nil, 403)
			if !strings.Contains(string(body), `"title":"Operations disabled"`) {
				t.Fatalf("wrong gate response for %s %s: %s", route.method, route.path, body)
			}
		}
	}
	checkDisabled()
	// Disabling the workspace does not hide existing responsibility labels or
	// disable independent service deployment actions and audit collection.
	serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/owner", nil, 200)
	serviceRequestTest(t, a, "GET", "/api/v1/services/missing/impact", nil, 404)
	serviceRequestTest(t, a, "POST", "/api/v1/services/missing/redeploy", nil, 404)
	serviceRequestTest(t, a, "POST", "/api/v1/projects", map[string]any{"name": "Audited while Operations disabled"}, 201)
	audit, err := a.store.(operationsStore).ListAuditEvents(ctx, core.AuditFilter{Action: "POST /api/v1/projects"})
	if err != nil || len(audit) != 1 || audit[0].Outcome != "succeeded" {
		t.Fatal("disabling Operations stopped audit collection", audit, err)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/settings", map[string]any{"operationsEnabled": true}, 200)
	for _, path := range []string{"/api/v1/audit", "/api/v1/operations/summary", "/api/v1/operations/ownership", "/api/v1/operations/backups", retention, "/api/v1/projects/" + app.ProjectID + "/owner-candidates", "/api/v1/identity-team-mappings"} {
		serviceRequestTest(t, a, "GET", path, nil, 200)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/settings", map[string]any{"operationsEnabled": false}, 200)
	checkDisabled()
}

func TestControllerSettingsOwnerPermissionsAndExplicitBoolean(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	member := core.User{ID: "settings-member", Username: "settings-member", SystemRole: core.UserRoleMember, State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, member); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		unauthenticated := httptest.NewRecorder()
		a.ServeHTTP(unauthenticated, httptest.NewRequest(method, "/api/v1/settings", strings.NewReader(`{"operationsEnabled":true}`)))
		if unauthenticated.Code != 401 {
			t.Fatal("settings accepted unauthenticated request", method, unauthenticated.Code)
		}
		r := tokenRequest(method, "/api/v1/settings", strings.NewReader(`{"operationsEnabled":true}`))
		r.Header.Set("Impersonate-User", member.ID)
		w := httptest.NewRecorder()
		a.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("member/impersonation bypassed owner settings permission", method, w.Code)
		}
	}
	for _, invalid := range []string{"", "null", "{}", `{"operationsEnabled":null}`, `{"operationsEnabled":"true"}`, `{"operationsEnabled":1}`, `{"operationsEnabled":[]}`, `{"operationsEnabled":true,"unknown":true}`, `{"operationsEnabled":true}{}`, `{"operationsEnabled":true}garbage`} {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, tokenRequest("PUT", "/api/v1/settings", strings.NewReader(invalid)))
		if w.Code != 400 {
			t.Fatalf("invalid settings body %q returned %d", invalid, w.Code)
		}
	}
	settings, err := a.store.GetControllerSettings(ctx)
	if err != nil || settings.OperationsEnabled {
		t.Fatal("rejected settings requests changed saved value", settings, err)
	}
	for _, enabled := range []bool{true, false} {
		serviceRequestTest(t, a, "PUT", "/api/v1/settings", map[string]any{"operationsEnabled": enabled}, 200)
		for _, impersonate := range []string{"", member.ID} {
			r := tokenRequest("GET", "/api/v1/overview", nil)
			r.Header.Set("Impersonate-User", impersonate)
			w := httptest.NewRecorder()
			a.ServeHTTP(w, r)
			var overview core.Overview
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &overview) != nil || overview.ControllerSettings.OperationsEnabled != enabled || !strings.Contains(w.Body.String(), `"controllerSettings"`) {
				t.Fatal("overview omitted saved settings for authenticated identity", w.Code, impersonate)
			}
		}
	}
}

func TestControllerSettingsOverviewWatchUpdates(t *testing.T) {
	a := serviceTestAPI(t)
	server := httptest.NewServer(a)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := func(method, path string, body string) *http.Request {
		r, err := http.NewRequestWithContext(ctx, method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer secret")
		return r
	}
	baseline, err := http.DefaultClient.Do(request("GET", "/api/v1/overview", ""))
	if err != nil {
		t.Fatal(err)
	}
	baseline.Body.Close()
	watch := request("GET", "/api/v1/overview/watch", "")
	watch.Header.Set("Last-Event-ID", baseline.Header.Get("X-Overview-Version"))
	response, err := http.DefaultClient.Do(watch)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	events := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				select {
				case events <- scanner.Text():
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	for _, enabled := range []string{"true", "false"} {
		result, err := http.DefaultClient.Do(request("PUT", "/api/v1/settings", `{"operationsEnabled":`+enabled+`}`))
		if err != nil {
			t.Fatal(err)
		}
		result.Body.Close()
		if result.StatusCode != 200 {
			t.Fatal("settings update failed", result.StatusCode)
		}
		select {
		case event := <-events:
			if !strings.Contains(event, `"path":"/controllerSettings/operationsEnabled"`) || !strings.Contains(event, `"value":`+enabled) {
				t.Fatal("settings change did not reach overview stream", event)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no settings change on overview stream")
		}
	}
}

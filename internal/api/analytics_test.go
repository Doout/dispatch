package api

import (
	"context"
	"github.com/doout/dispatch/internal/analytics"
	"github.com/doout/dispatch/internal/core"
	"net/http/httptest"
	"testing"
	"time"
)

type analyticsProbe struct {
	projects map[string]bool
	calls    int
}

func (p *analyticsProbe) Summary(projects map[string]bool, days int) analytics.Summary {
	p.projects = projects
	p.calls++
	return analytics.Summary{State: "ready", Days: days, Daily: []analytics.Day{}}
}
func TestAnalyticsUsesCurrentProjectPermissions(t *testing.T) {
	reader := &analyticsProbe{}
	handler, cleanup := testHandlerWithEventConfig(t, AuthConfig{AdminToken: "secret"}, false, EventConfig{Analytics: reader})
	defer cleanup()
	a := handler.(*API)
	ctx := context.Background()
	now := time.Now()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := core.User{ID: "reader", Username: "reader", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateUser(ctx, user))
	for _, id := range []string{"allowed", "hidden"} {
		must(a.store.CreateProject(ctx, core.Project{ID: id, Name: id, CreatedAt: now}))
	}
	must(a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "grant", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: "allowed", Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}))
	request := func(path string, auth bool) int {
		r := httptest.NewRequest("GET", path, nil)
		if auth {
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set("Impersonate-User", user.ID)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if status := request("/api/v1/analytics?days=30", false); status != 401 {
		t.Fatalf("unauthorized status %d", status)
	}
	if status := request("/api/v1/analytics?days=30", true); status != 200 {
		t.Fatalf("status %d", status)
	}
	if !reader.projects["allowed"] || reader.projects["hidden"] || reader.calls != 1 {
		t.Fatalf("wrong scope: %+v", reader)
	}
	if status := request("/api/v1/analytics?days=999", true); status != 400 {
		t.Fatalf("range status %d", status)
	}
	if status := request("/api/v1/overview", true); status != 200 {
		t.Fatalf("overview %d", status)
	}
	if reader.calls != 1 {
		t.Fatal("overview waited on analytics")
	}
}

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

type revokeDeploymentStream struct {
	*httptest.ResponseRecorder
	onFirstFlush func()
}

func (w *revokeDeploymentStream) Flush() {
	w.ResponseRecorder.Flush()
	if w.onFirstFlush != nil {
		fn := w.onFirstFlush
		w.onFirstFlush = nil
		fn()
	}
}

func TestDeploymentStreamStopsWhenProjectAccessIsRevoked(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, err := a.store.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := core.User{ID: "stream-member", Username: "stream-member", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	server := core.Server{ID: "stream-server", Name: "stream-server", Runtime: "docker", CreatedAt: now}
	if err := a.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	app := core.App{ID: "stream-app", Name: "stream-app", ProjectID: projects[0].ID, ServerID: server.ID, SourceCredentialID: "private-source", CreatedAt: now}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	deployment := core.Deployment{ID: "stream-deployment", AppID: app.ID, App: &app, State: core.DeploymentQueued, CreatedAt: now}
	if err := a.store.CreateDeployment(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "stream-grant", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: app.ProjectID, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/deployments/"+deployment.ID+"/events", nil)
	ctx = withIdentity(r.Context(), identityForUser(user))
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	route := chi.NewRouteContext()
	route.URLParams.Add("id", deployment.ID)
	r = r.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	w := &revokeDeploymentStream{ResponseRecorder: httptest.NewRecorder()}
	w.onFirstFlush = func() {
		if err := a.store.DeleteRoleAssignment(ctx, grant.ID); err != nil {
			t.Fatal(err)
		}
	}
	a.deploymentEvents(w, r)
	body := w.Body.String()
	if !strings.Contains(body, "event: state") || !strings.Contains(body, "event: auth-error\ndata: 403") {
		t.Fatalf("stream did not close on revocation: %s", body)
	}
	if strings.Count(body, "event: state") != 1 {
		t.Fatal("deployment data escaped after revocation")
	}
	if strings.Contains(body, "private-source") {
		t.Fatal("member stream exposed source credential reference")
	}
}

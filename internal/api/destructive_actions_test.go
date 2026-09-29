package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/deploy"
)

// Existing lifecycle tests use this helper to act with a reviewed confirmation.
// Missing, incorrect and stale confirmation cases below send raw requests.
func confirmedTokenRequest(t *testing.T, handler http.Handler, method, target string, body io.Reader) *http.Request {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodDelete {
		u.Path += "/delete-preview"
	} else {
		u.Path += "-preview"
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, tokenRequest(http.MethodPost, u.String(), nil))
	if rr.Code != 200 {
		t.Fatalf("review %s: %d %s", target, rr.Code, rr.Body.String())
	}
	var review destructiveReview
	if err = json.Unmarshal(rr.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	fields := map[string]any{}
	if body != nil {
		raw, _ := io.ReadAll(body)
		if len(raw) > 0 && string(raw) != "null" {
			if err = json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
		}
	}
	fields["confirmation"] = destructiveConfirmation{ResourceID: review.ResourceID, Action: review.Action, ExpectedVersion: review.Version, ConfirmName: review.Name}
	raw, _ := json.Marshal(fields)
	return tokenRequest(method, target, bytes.NewReader(raw))
}

func TestDestructiveConfirmationRejectsMissingWrongAndStaleRequests(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	servers, _ := a.store.ListServers(ctx)
	app := core.App{ID: "confirm-delete-app", Name: "Confirm deletion", ProjectID: projects[0].ID, ServerID: servers[0].ID, BuildType: core.BuildTypeDockerfile, CreatedAt: time.Now().UTC()}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/apps/" + app.ID
	call := func(req *http.Request, want int) {
		t.Helper()
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, req)
		if rr.Code != want {
			t.Fatalf("wanted %d, got %d: %s", want, rr.Code, rr.Body.String())
		}
	}
	call(tokenRequest("DELETE", path, nil), 422)
	wrong := confirmedTokenRequest(t, a, "DELETE", path, nil)
	raw, _ := io.ReadAll(wrong.Body)
	call(tokenRequest("DELETE", path, strings.NewReader(strings.ReplaceAll(string(raw), "Confirm deletion", "Different resource"))), 422)
	stale := confirmedTokenRequest(t, a, "DELETE", path, nil)
	app.Domain = "changed.example.test"
	if err := a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	call(stale, 409)
	if _, err := a.store.GetApp(ctx, app.ID); err != nil {
		t.Fatal("invalid confirmation deleted the application")
	}
	call(confirmedTokenRequest(t, a, "DELETE", path, nil), 204)
	audit := a.store.(operationsStore)
	rows, err := audit.ListAuditEvents(ctx, core.AuditFilter{ProjectIDs: []string{app.ProjectID}, AppID: app.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	matched := false
	for _, row := range rows {
		if row.ConfirmedAction == "delete" && row.Outcome == "succeeded" {
			matched = row.ResourceID == app.ID && row.ConfirmedName == app.Name && row.ConfirmedVersion != ""
		}
	}
	if !matched {
		t.Fatal("confirmed resource and project scope were lost after deletion")
	}
}

func TestDestructiveReviewUsesStableTargetIdentityAndProtectsRoles(t *testing.T) {
	a := serviceTestAPI(t)
	apps, _ := a.store.ListApps(context.Background())
	path := "/api/v1/apps/" + apps[0].ID + "/delete-preview"
	first := serviceRequestTest(t, a, "POST", path, nil, 200)
	second := serviceRequestTest(t, a, "POST", path, nil, 200)
	if !bytes.Equal(first, second) {
		t.Fatal("equivalent reviews changed without a resource mutation")
	}
	req := tokenRequest("POST", path, nil)
	req.Header.Set("Authorization", "Bearer invalid-token")
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatal("unauthenticated caller could review destructive resources")
	}
}

func TestDestructiveReviewPreservesProjectAuthorization(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := core.User{ID: "confirmation-viewer", Username: "confirmation-viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	if err = a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err = a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "confirmation-viewer-role", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: apps[0].ProjectID, Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"delete-preview", "cleanup-preview"} {
		req := tokenRequest("POST", "/api/v1/apps/"+apps[0].ID+"/"+action, nil)
		req.Header.Set("Impersonate-User", user.ID)
		rr := httptest.NewRecorder()
		a.ServeHTTP(rr, req)
		if rr.Code != 403 {
			t.Fatalf("viewer destructive review: %d %s", rr.Code, rr.Body.String())
		}
	}
	req := confirmedTokenRequest(t, a, "DELETE", "/api/v1/apps/"+apps[0].ID, nil)
	req.Header.Set("Impersonate-User", user.ID)
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatal("confirmation bypassed project authorization")
	}
	if _, err = a.store.GetApp(ctx, apps[0].ID); err != nil {
		t.Fatal("viewer deleted application")
	}
}

type confirmedCleanupRecorder struct {
	calls    int
	err      error
	identity string
}

func (e *confirmedCleanupRecorder) Deploy(context.Context, core.Deployment, core.App, core.Server, deploy.Progress) error {
	return nil
}
func (e *confirmedCleanupRecorder) Cleanup(context.Context, core.App, core.Server, deploy.Progress) error {
	e.calls++
	return e.err
}

func (e *confirmedCleanupRecorder) CurrentRuntimeIdentity(context.Context, core.App, core.Server) (string, error) {
	return e.identity, nil
}
func (e *confirmedCleanupRecorder) PreviewRuntimeRollback(context.Context, core.Deployment, core.App, core.Server) (deploy.RollbackPreview, error) {
	return deploy.RollbackPreview{Available: true, RuntimeDigest: e.identity}, nil
}
func (e *confirmedCleanupRecorder) RollbackRuntime(context.Context, core.Deployment, core.Deployment, core.App, core.Server, deploy.Progress) error {
	return errors.New("rollback is not part of this cleanup fixture")
}

func TestDestructiveCleanupFailureKeepsRegistrationAndRecordsOutcome(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	projects, _ := a.store.ListProjects(ctx)
	servers, _ := a.store.ListServers(ctx)
	now := time.Now().UTC()
	app := core.App{ID: "cleanup-reviewed-app", Name: "Cleanup review", ProjectID: projects[0].ID, ServerID: servers[0].ID, BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	if err := a.store.CreateDeployment(ctx, core.Deployment{ID: "cleanup-success", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	recorder := &confirmedCleanupRecorder{err: errors.New("one container remains"), identity: "original-runtime"}
	a.deploy = deploy.NewService(a.store, recorder)
	path := "/api/v1/apps/" + app.ID
	rr := httptest.NewRecorder()
	a.ServeHTTP(rr, tokenRequest("DELETE", path, nil))
	if rr.Code != 422 || recorder.calls != 0 {
		t.Fatal("missing confirmation reached runtime cleanup")
	}
	stale := confirmedTokenRequest(t, a, "DELETE", path, nil)
	recorder.identity = "external-runtime-change"
	rr = httptest.NewRecorder()
	a.ServeHTTP(rr, stale)
	if rr.Code != 409 || recorder.calls != 0 {
		t.Fatal("external runtime change did not invalidate cleanup confirmation")
	}
	rr = httptest.NewRecorder()
	a.ServeHTTP(rr, confirmedTokenRequest(t, a, "DELETE", path, nil))
	if rr.Code != 409 || recorder.calls != 1 {
		t.Fatalf("cleanup failure not reported: %d %s", rr.Code, rr.Body.String())
	}
	if _, err := a.store.GetApp(ctx, app.ID); err != nil {
		t.Fatal("failed cleanup discarded registration")
	}
	events, err := a.store.(operationsStore).ListAuditEvents(ctx, core.AuditFilter{ProjectIDs: []string{app.ProjectID}, AppID: app.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.ConfirmedName == app.Name && event.Outcome == "failed" && event.ActorID != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("cleanup failure audit missing")
	}
	recorder.err = nil
	rr = httptest.NewRecorder()
	a.ServeHTTP(rr, confirmedTokenRequest(t, a, "DELETE", path, nil))
	if rr.Code != 204 || recorder.calls != 2 {
		t.Fatalf("reviewed cleanup retry failed: %d %s", rr.Code, rr.Body.String())
	}
}

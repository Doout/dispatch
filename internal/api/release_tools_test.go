package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReleaseNotesActivityAndRollbackConfirmation(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	d := core.Deployment{ID: "release-api-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CommitSHA: "abcd1234", CreatedAt: time.Now().UTC()}
	if err = a.store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/deployments/"+d.ID+"/release", map[string]any{"notes": "Adds reviewable release context", "links": []string{"https://example.test/repo/pull/1"}}, 200)
	raw := serviceRequestTest(t, a, "GET", "/api/v1/deployments/"+d.ID+"/release", nil, 200)
	var result deploymentReleaseResponse
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Note.Notes != "Adds reviewable release context" {
		t.Fatal("notes not retained")
	}
	serviceRequestTest(t, a, "PUT", "/api/v1/deployments/"+d.ID+"/release", map[string]any{"links": []string{"javascript:alert(1)"}}, 422)
	serviceRequestTest(t, a, "PUT", "/api/v1/deployments/"+d.ID+"/release", map[string]any{"links": []string{"https://user:password@example.test/repo"}}, 422)
	serviceRequestTest(t, a, "POST", "/api/v1/deployments/"+d.ID+"/rollback", map[string]any{"confirmDeploymentId": d.ID}, 422)
	raw = serviceRequestTest(t, a, "GET", "/api/v1/apps/"+app.ID+"/activity", nil, 200)
	if !strings.Contains(string(raw), "release.notes") || !strings.Contains(string(raw), d.ID) {
		t.Fatal("activity omitted deployment or operator action")
	}
}

func TestReviewedDeploymentRejectsStaleInputs(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, err := a.store.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	app := apps[0]
	app.ID, app.Name, app.Template = "reviewed-api-app", "reviewed-api-app", false
	if err = a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	review := core.DeploymentReview{ExpectedAppName: app.Name, ProjectID: app.ProjectID, AppSpecDigest: app.SpecDigest(), BindingsDigest: core.ServiceBindingConfigurationDigest(nil), ServiceRevisions: map[string]int64{}}
	app.Name = "renamed-after-preview"
	if app.SpecDigest() != review.AppSpecDigest {
		t.Fatal("rename fixture must isolate name from settings digest")
	}
	if err = a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/apps/"+app.ID+"/deployments", map[string]any{"commitSha": "abcdef1234", "review": review}, 409)
	review.ExpectedAppName = app.Name
	app.Domain = "changed.example.test"
	if err = a.store.UpdateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	serviceRequestTest(t, a, "POST", "/api/v1/apps/"+app.ID+"/deployments", map[string]any{"commitSha": "abcdef1234", "review": review}, 409)
	serviceRequestTest(t, a, "POST", "/api/v1/apps/"+app.ID+"/deployments", map[string]any{"review": map[string]any{}}, 422)
	history, err := a.store.ListApplicationHistory(ctx, app.ID, "", 10)
	if err != nil || len(history) != 0 {
		t.Fatal("rejected review created a deployment")
	}
}

func TestReleaseRoutesEnforceProjectRoles(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	apps, _ := a.store.ListApps(ctx)
	app := apps[0]
	now := time.Now().UTC()
	user := core.User{ID: "release-viewer", Username: "release-viewer", SystemRole: "member", State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	grant := core.RoleAssignment{ID: "release-grant", PrincipalType: "user", PrincipalID: user.ID, ScopeType: "project", ScopeID: app.ProjectID, Role: "viewer", CreatedAt: now, UpdatedAt: now}
	if err := a.store.UpsertRoleAssignment(ctx, grant); err != nil {
		t.Fatal(err)
	}
	d := core.Deployment{ID: "release-role-deployment", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err := a.store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	call := func(handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		req = req.WithContext(withIdentity(req.Context(), core.Identity{ID: user.ID, SystemRole: "member"}))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
		rr := httptest.NewRecorder()
		handler(rr, req)
		return rr
	}
	for _, handler := range []http.HandlerFunc{a.deploymentPermission(core.PermissionDeploymentRun)(http.HandlerFunc(a.rollbackDeploymentRelease)).ServeHTTP, a.deploymentPermission(core.PermissionProjectConfigure)(http.HandlerFunc(a.updateDeploymentRelease)).ServeHTTP} {
		if rr := call(handler, d.ID); rr.Code != 403 {
			t.Fatalf("viewer mutation returned %d", rr.Code)
		}
	}
	if rr := call(a.deploymentPermission(core.PermissionProjectView)(http.HandlerFunc(a.getDeploymentRelease)).ServeHTTP, d.ID); rr.Code != 200 {
		t.Fatal("viewer cannot inspect release metadata")
	}
	grant.ScopeID = "unrelated-project"
	if err := a.store.DeleteRoleAssignment(ctx, grant.ID); err != nil {
		t.Fatal(err)
	}
	if rr := call(a.deploymentPermission(core.PermissionProjectView)(http.HandlerFunc(a.getDeploymentRelease)).ServeHTTP, d.ID); rr.Code != 403 {
		t.Fatal("cross-project metadata exposed")
	}
}

func TestDiagnosisFindsActionableFailuresAndRedacts(t *testing.T) {
	pod := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api"}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "api", RestartCount: 4, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}}}
	issues := podDiagnosis(pod)
	if len(issues) != 1 || !strings.Contains(issues[0].NextStep, "registry") || issues[0].Restarts != 4 {
		t.Fatal("image pull failure has no useful next step")
	}
	redact := diagnosticRedactor([]string{"local-credential-value"})
	safe := redact("dsn=postgresql://user:local-credential-value@db/app password=other-value token=another-value")
	for _, secret := range []string{"local-credential-value", "other-value", "another-value"} {
		if strings.Contains(safe, secret) {
			t.Fatal("diagnostic credential leaked")
		}
	}
	if link := commitSourceLink("git@github.example.test:team/repo.git", "abcdef123456"); link != "https://github.example.test/team/repo/commit/abcdef123456" {
		t.Fatalf("invalid source link %q", link)
	}
	if commitSourceLink("https://example.test/repo", "HEAD") != "" {
		t.Fatal("mutable branch presented as immutable commit")
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/go-chi/chi/v5"
)

func approvalFixture(t *testing.T, a *API, prefix string, at time.Time) (core.Project, core.WorkflowResource, core.WorkflowRevision, core.WorkflowStageRun) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	project := core.Project{ID: prefix + "-project", Name: prefix, CreatedAt: at}
	must(a.store.CreateProject(ctx, project))
	secret := core.Secret{ID: prefix + "-secret", Name: prefix, Type: core.SecretTypeGitHubToken, EnvironmentVariable: prefix + "-token", EncryptedValue: "fixture", CreatedAt: at}
	must(a.store.CreateSecret(ctx, secret))
	source := core.ConfigSource{ID: prefix + "-source", ProjectID: project.ID, Name: prefix, Repository: "example/repository", CredentialSecretID: secret.ID, CreatedAt: at, UpdatedAt: at}
	must(a.store.CreateConfigSource(ctx, source))
	resource := core.WorkflowResource{ID: prefix + "-resource", ConfigSourceID: source.ID, Name: prefix, Kind: "Application", Path: "app.yaml", State: "valid", Active: true, CreatedAt: at, UpdatedAt: at}
	must(a.store.CreateWorkflowResource(ctx, resource))
	revision := core.WorkflowRevision{ID: prefix + "-revision", ResourceID: resource.ID, State: "awaiting_approval", ConfigSHA: "reviewed-config-sha", SpecDigest: "reviewed-spec", CreatedAt: at}
	must(a.store.CreateWorkflowRevision(ctx, revision))
	stage := core.WorkflowStageRun{ID: prefix + "-stage", RevisionID: revision.ID, StageName: "production", State: "awaiting_approval", Approval: "required", CreatedAt: at}
	must(a.store.CreateWorkflowStageRun(ctx, stage))
	return project, resource, revision, stage
}

func TestOverviewKeepsPendingApprovalsBeyondRecentRevisionsAndProjectScope(t *testing.T) {
	a := serviceTestAPI(t)
	ctx := context.Background()
	now := time.Now().UTC()
	project, resource, pending, pendingStage := approvalFixture(t, a, "visible-approval", now.Add(-48*time.Hour))
	_, _, privateRevision, privateStage := approvalFixture(t, a, "private-approval", now.Add(-48*time.Hour))
	for i := 0; i < 101; i++ {
		if err := a.store.CreateWorkflowRevision(ctx, core.WorkflowRevision{ID: fmt.Sprintf("newer-revision-%03d", i), ResourceID: resource.ID, State: "succeeded", CreatedAt: now.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	recent, err := a.store.ListWorkflowRevisions(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, revision := range recent {
		if revision.ID == pending.ID {
			t.Fatal("fixture did not move pending approval outside recent history")
		}
	}
	user := core.User{ID: "pending-approver", Username: "pending-approver", SystemRole: core.UserRoleMember, State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "pending-grant", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: project.ID, Role: "admin", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []bool{true, false} {
		request := tokenRequest(http.MethodGet, "/api/v1/overview", nil)
		if !owner {
			request.Header.Set("Impersonate-User", user.ID)
		}
		response := httptest.NewRecorder()
		a.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("overview: %d %s", response.Code, response.Body.String())
		}
		var overview core.Overview
		if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
			t.Fatal(err)
		}
		foundRevision, foundStage := false, false
		for _, revision := range overview.WorkflowRevisions {
			if revision.ID == pending.ID {
				foundRevision = true
				if revision.ConfigSHA != pending.ConfigSHA {
					t.Fatal("approval immutable context changed")
				}
			}
			if !owner && revision.ID == privateRevision.ID {
				t.Fatal("foreign pending revision exposed")
			}
		}
		for _, stage := range overview.WorkflowStageRuns {
			if stage.ID == pendingStage.ID {
				foundStage = true
			}
			if !owner && stage.ID == privateStage.ID {
				t.Fatal("foreign pending stage exposed")
			}
		}
		if !foundRevision || !foundStage {
			t.Fatalf("owner=%t pending approval or review context disappeared", owner)
		}
	}
}

func TestApprovalAuditUsesWorkflowProjectAndFiltersForeignActions(t *testing.T) {
	a := serviceTestAPI(t)
	enableOperationsForTest(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	project, _, _, stage := approvalFixture(t, a, "audit-approval", now)
	_, _, _, foreignStage := approvalFixture(t, a, "foreign-audit-approval", now)
	user := core.User{ID: "audit-approver", Username: "audit-approver", DisplayName: "Release Approver", SystemRole: core.UserRoleMember, State: "active", CreatedAt: now, UpdatedAt: now}
	if err := a.store.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "approval-audit-grant", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: project.ID, Role: "admin", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Use(a.auditMutation)
	router.With(a.workflowStagePermission(core.PermissionStageApprove)).Post("/api/v1/workflow/stages/{id}/approve", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusAccepted) })
	for _, item := range []struct {
		id     string
		status int
	}{{stage.ID, 202}, {foreignStage.ID, 403}} {
		request := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/stages/"+item.id+"/approve", nil)
		request = request.WithContext(withIdentity(request.Context(), identityForUser(user)))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != item.status {
			t.Fatalf("approval permission: %d", response.Code)
		}
	}
	request := tokenRequest(http.MethodGet, "/api/v1/audit", nil)
	request.Header.Set("Impersonate-User", user.ID)
	response := httptest.NewRecorder()
	a.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var events []core.AuditEvent
	if err := json.Unmarshal(response.Body.Bytes(), &events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ProjectID != project.ID || events[0].ResourceID != stage.ID || events[0].ActorID != user.ID || events[0].ActorName != user.DisplayName || events[0].Outcome != "succeeded" {
		t.Fatalf("project approval audit missing or incorrectly scoped: %+v", events)
	}
	apps, err := a.store.ListApps(ctx)
	if err != nil || len(apps) == 0 {
		t.Fatal("application fixture unavailable", err)
	}
	app := apps[0]
	app.ID, app.Name, app.ProjectID = "approved-application", "approved-application", project.ID
	if err := a.store.CreateApp(ctx, app); err != nil {
		t.Fatal(err)
	}
	d := core.Deployment{ID: "approved-application-release", AppID: app.ID, State: core.DeploymentSucceeded, CreatedAt: now}
	if err := a.store.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	// A stage can deploy several apps. Associate its audit by retained IDs and
	// reject foreign workflow records even if they contain a matching ID.
	stage.DeploymentIDs = []string{d.ID, "another-application-release"}
	foreignStage.DeploymentIDs = []string{d.ID}
	for _, item := range []core.WorkflowStageRun{stage, foreignStage} {
		if err := a.store.UpdateWorkflowStageRun(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	request = tokenRequest(http.MethodGet, "/api/v1/apps/"+app.ID+"/activity", nil)
	request.Header.Set("Impersonate-User", user.ID)
	response = httptest.NewRecorder()
	a.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var activity struct {
		Items []releaseActivity `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &activity); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range activity.Items {
		if item.Kind == "audit" {
			if item.ID != events[0].ID || item.Actor != user.DisplayName || item.DeploymentID != d.ID {
				t.Fatalf("approval actor attached to wrong application: %+v", item)
			}
			found = true
		}
		if item.ID == foreignStage.ID {
			t.Fatal("foreign stage leaked through a matching deployment ID")
		}
	}
	if !found {
		t.Fatal("application timeline omitted the approval actor")
	}
}

package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

type evaluationVisibilityStore struct {
	store.Store
	queried []string
}

func (s *evaluationVisibilityStore) GetWorkflowEquivalence(_ context.Context, id string) (core.WorkflowEquivalence, error) {
	s.queried = append(s.queried, id)
	if id == "stale-evaluation" {
		return core.WorkflowEquivalence{}, store.ErrNotFound
	}
	return core.WorkflowEquivalence{
		ResourceID: id, BaselineRevisionID: "retained-workflow", ProjectID: "private-project-anchor",
		SpecDigest: "private-spec-digest", AppProofs: map[string]string{"app": "private-app-proof"},
		Sources:   map[string]core.WorkflowSourceRevision{"chart": {Alias: "chart", CommitSHA: "evaluated-chart-commit"}},
		Results:   []core.WorkflowDeploymentResult{{AppID: "app", DeploymentID: "retained-deployment", Outcome: "unchanged"}},
		CheckedAt: time.Now().UTC(),
	}, nil
}

func TestWorkflowEvaluationsRespectVisibilityAndHidePrivateAnchors(t *testing.T) {
	handler, cleanup := testHandlerWithDemo(t, AuthConfig{AdminToken: "secret"}, false)
	defer cleanup()
	a := handler.(*API)
	ctx := context.Background()
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	user := core.User{ID: "evaluation-viewer", Username: "evaluation-viewer", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	must(a.store.CreateUser(ctx, user))
	must(a.store.CreateSecret(ctx, core.Secret{ID: "evaluation-source-auth", Name: "evaluation-source-auth", Type: core.SecretTypeGitHubToken, CreatedAt: now, UpdatedAt: now}))
	for _, project := range []string{"visible-evaluation", "hidden-evaluation"} {
		must(a.store.CreateProject(ctx, core.Project{ID: project, Name: project, CreatedAt: now}))
		must(a.store.CreateConfigSource(ctx, core.ConfigSource{ID: project, ProjectID: project, Name: project, Repository: "fixture/" + project, CredentialSecretID: "evaluation-source-auth", Active: true, State: "synced", CreatedAt: now, UpdatedAt: now}))
		must(a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: project, ConfigSourceID: project, Name: project, Kind: "Application", Active: true, State: "ready", CreatedAt: now, UpdatedAt: now}))
	}
	for _, id := range []string{"stale-evaluation", "invalid-evaluation"} {
		state := "ready"
		if id == "invalid-evaluation" {
			state = "invalid"
		}
		must(a.store.CreateWorkflowResource(ctx, core.WorkflowResource{ID: id, ConfigSourceID: "visible-evaluation", Name: id, Kind: "Application", Active: true, State: state, CreatedAt: now, UpdatedAt: now}))
	}
	must(a.store.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: user.ID, PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: "visible-evaluation", Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now}))
	wrapper := &evaluationVisibilityStore{Store: a.store}
	a.store = wrapper
	for _, path := range []string{"/api/v1/overview", "/api/v1/workflow/resources"} {
		t.Run(path, func(t *testing.T) {
			wrapper.queried = nil
			r := httptest.NewRequest("GET", path, nil)
			r.Header.Set("Authorization", "Bearer secret")
			r.Header.Set("Impersonate-User", user.ID)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			body := w.Body.String()
			if strings.Count(body, `"lastEvaluation":`) != 1 || !strings.Contains(body, "evaluated-chart-commit") || !strings.Contains(body, "retained-deployment") {
				t.Fatal("missing current evaluation or included stale evaluation", body)
			}
			for _, hidden := range []string{"hidden-evaluation", "private-project-anchor", "private-spec-digest", "private-app-proof"} {
				if strings.Contains(body, hidden) {
					t.Fatalf("response exposed %q", hidden)
				}
			}
			if len(wrapper.queried) != 2 {
				t.Fatal("unexpected evaluation reads", wrapper.queried)
			}
			for _, id := range wrapper.queried {
				if id != "visible-evaluation" && id != "stale-evaluation" {
					t.Fatal("read inaccessible or invalid evaluation", id)
				}
			}
		})
	}
}

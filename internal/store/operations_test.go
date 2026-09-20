package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestOperationsPersistenceAndProtection(t *testing.T) {
	testOperations(t, filepath.Join(t.TempDir(), "operations.db"))
}
func TestOperationsPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("DISPATCH_OPERATIONS_POSTGRES_URL")
	if dsn == "" {
		t.Skip("set DISPATCH_OPERATIONS_POSTGRES_URL to a disposable database")
	}
	testOperations(t, dsn)
}
func testOperations(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	must(s.CreateProject(ctx, core.Project{ID: "ops-project", Name: "ops-project", CreatedAt: now}))
	must(s.CreateServer(ctx, core.Server{ID: "ops-server", Name: "ops-server", Runtime: "docker", CreatedAt: now}))
	app := core.App{ID: "ops-app", ProjectID: "ops-project", ServerID: "ops-server", Name: "ops-app", BuildType: core.BuildTypeDockerfile, CreatedAt: now}
	must(s.CreateApp(ctx, app))
	for i := 0; i < 12; i++ {
		at := now.AddDate(0, 0, -200+i)
		state := core.DeploymentFailed
		if i == 0 || i == 6 {
			state = core.DeploymentSucceeded
		}
		d := core.Deployment{ID: fmt.Sprintf("ops-run-%02d", i), AppID: app.ID, State: state, CreatedAt: at, FinishedAt: &at}
		must(s.CreateDeployment(ctx, d))
		must(s.AppendDeploymentLog(ctx, core.DeploymentLog{DeploymentID: d.ID, Level: "info", Message: "test log", CreatedAt: at}))
	}
	must(s.SaveDriftBaseline(ctx, core.DriftBaseline{DeploymentID: "ops-run-01", AppID: app.ID, ServerID: "ops-server", Ciphertext: "retained-encrypted-baseline"}))
	policy := core.RetentionPolicy{ProjectID: app.ProjectID, LogDays: 30, RunDays: 90, KeepRuns: 5}
	must(s.SaveRetentionPolicy(ctx, policy))
	saved, err := s.GetRetentionPolicy(ctx, app.ProjectID)
	must(err)
	if saved != policy {
		t.Fatal("retention policy lost")
	}
	preview, err := s.ApplyRetention(ctx, policy, false, now)
	must(err)
	if preview.Runs != 4 {
		t.Fatalf("unsafe retention preview: %+v", preview)
	}
	if _, err = s.GetDeployment(ctx, "ops-run-02"); err != nil {
		t.Fatal("preview removed a run")
	}
	applied, err := s.ApplyRetention(ctx, policy, true, now)
	must(err)
	if applied.Runs != preview.Runs {
		t.Fatal("apply differed from preview")
	}
	for _, id := range []string{"ops-run-00", "ops-run-01", "ops-run-06", "ops-run-11"} {
		if _, err = s.GetDeployment(ctx, id); err != nil {
			t.Fatalf("protected %s removed: %v", id, err)
		}
	}
	if _, err = s.GetDeployment(ctx, "ops-run-02"); !errors.Is(err, ErrNotFound) {
		t.Fatal("eligible failed run retained")
	}
	expired := now.Add(-time.Minute)
	future := now.Add(time.Hour)
	for _, d := range []core.Deployment{{ID: "ops-expired", AppID: app.ID, State: core.DeploymentBuilding, CreatedAt: now.Add(-time.Hour), LeaseUntil: &expired}, {ID: "ops-active", AppID: app.ID, State: core.DeploymentBuilding, CreatedAt: now, LeaseUntil: &future}} {
		must(s.CreateDeployment(ctx, d))
	}
	recovered, err := s.RecoverInterruptedDeployments(ctx, now, now)
	must(err)
	if len(recovered) != 1 || recovered[0].ID != "ops-expired" {
		t.Fatalf("wrong recovery: %+v", recovered)
	}
	again, err := s.RecoverInterruptedDeployments(ctx, now, now)
	must(err)
	if len(again) != 0 {
		t.Fatal("replayed recovery")
	}
	active, err := s.GetDeployment(ctx, "ops-active")
	must(err)
	if active.State != core.DeploymentBuilding {
		t.Fatal("live worker was interrupted")
	}
	for _, e := range []core.AuditEvent{{ID: "ops-a", ProjectID: app.ProjectID, ActorID: "alice", AppID: app.ID}, {ID: "ops-b", ProjectID: "other-project", ActorID: "bob"}} {
		e.CreatedAt = now
		e.Action = "PUT /apps/{id}"
		e.Outcome = "succeeded"
		must(s.AppendAuditEvent(ctx, e))
	}
	events, err := s.ListAuditEvents(ctx, core.AuditFilter{ProjectIDs: []string{app.ProjectID}})
	must(err)
	if len(events) != 1 || events[0].ActorID != "alice" {
		t.Fatal("audit project filter leaked rows")
	}
	events, err = s.ListAuditEvents(ctx, core.AuditFilter{ProjectIDs: []string{}})
	must(err)
	if len(events) != 0 {
		t.Fatal("empty project scope exposed audit")
	}
	must(s.CreateUser(ctx, core.User{ID: "ops-user", Username: "ops-user", State: "active", SystemRole: "member", CreatedAt: now, UpdatedAt: now}))
	grant := core.RoleAssignment{ID: "ops-grant", PrincipalType: "user", PrincipalID: "ops-user", ScopeType: "project", ScopeID: app.ProjectID, Role: "operator", CreatedAt: now, UpdatedAt: now, ExpiresAt: &future}
	must(s.UpsertRoleAssignment(ctx, grant))
	grants, err := s.ListRoleAssignments(ctx)
	must(err)
	if len(grants) != 1 || grants[0].ExpiresAt == nil || !grants[0].ExpiresAt.Equal(future) {
		t.Fatal("grant expiry not persisted")
	}
	grant.ExpiresAt = nil
	must(s.UpsertRoleAssignment(ctx, grant))
	grants, err = s.ListRoleAssignments(ctx)
	must(err)
	if grants[0].ExpiresAt != nil {
		t.Fatal("cannot remove grant expiry")
	}
	must(s.CreateTeam(ctx, core.Team{ID: "ops-team", Name: "ops-team", CreatedAt: now, UpdatedAt: now}))
	must(s.CreateAuthProvider(ctx, core.AuthProvider{ID: "ops-provider", Name: "ops-provider", Type: "github", Provisioning: "existing", State: "ready", CreatedAt: now, UpdatedAt: now}))
	must(s.SaveIdentityTeamMapping(ctx, core.IdentityTeamMapping{ID: "ops-map", ProviderID: "ops-provider", ExternalGroup: "org/operators", TeamID: "ops-team"}))
	must(s.UpsertExternalIdentity(ctx, core.ExternalIdentity{ProviderID: "ops-provider", Subject: "ops-subject", UserID: "ops-user", Login: "ops-user", LastLogin: now, CreatedAt: now}))
	must(s.SyncIdentityTeamMemberships(ctx, "ops-provider", "ops-user", []string{"org/operators"}))
	members, err := s.ListIdentityTeamMembers(ctx)
	must(err)
	if len(members) != 1 {
		t.Fatal("provider mapping not applied")
	}
	must(s.SyncIdentityTeamMemberships(ctx, "ops-provider", "ops-user", nil))
	members, err = s.ListIdentityTeamMembers(ctx)
	must(err)
	if len(members) != 0 {
		t.Fatal("removed provider access persisted")
	}
}

func TestIdentityMappedAccessEndsWhenUnlinkedOrProviderDisabled(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "identity-ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Migrate(ctx))
	now := time.Now().UTC()
	user := core.User{ID: "mapped-user", Username: "mapped-user", State: "active", SystemRole: "member", CreatedAt: now, UpdatedAt: now}
	must(s.CreateUser(ctx, user))
	must(s.CreateTeam(ctx, core.Team{ID: "mapped-team", Name: "mapped-team", CreatedAt: now, UpdatedAt: now}))
	provider := core.AuthProvider{ID: "mapped-provider", Name: "mapped-provider", Type: "github", Provisioning: "existing", State: "ready", CreatedAt: now, UpdatedAt: now}
	must(s.CreateAuthProvider(ctx, provider))
	must(s.SaveIdentityTeamMapping(ctx, core.IdentityTeamMapping{ID: "mapped-grant", ProviderID: provider.ID, ExternalGroup: "org/admins", TeamID: "mapped-team"}))
	identity := core.ExternalIdentity{ProviderID: provider.ID, Subject: "subject-one", UserID: user.ID, Login: "mapped-user", LastLogin: now, CreatedAt: now}
	must(s.UpsertExternalIdentity(ctx, identity))
	must(s.SyncIdentityTeamMemberships(ctx, provider.ID, user.ID, []string{"org/admins"}))
	assertCount := func(want int) {
		t.Helper()
		members, err := s.ListIdentityTeamMembers(ctx)
		must(err)
		if len(members) != want {
			t.Fatalf("got %d memberships, want %d", len(members), want)
		}
	}
	assertCount(1)
	provider.State = "disabled"
	must(s.UpdateAuthProvider(ctx, provider))
	assertCount(0)
	provider.State = "ready"
	must(s.UpdateAuthProvider(ctx, provider))
	assertCount(1)
	must(s.DeleteExternalIdentity(ctx, provider.ID, user.ID))
	assertCount(0)
	identity.Subject = "different-person"
	must(s.UpsertExternalIdentity(ctx, identity))
	assertCount(0)
	must(s.SyncIdentityTeamMemberships(ctx, provider.ID, user.ID, []string{"org/admins"}))
	assertCount(1)
}

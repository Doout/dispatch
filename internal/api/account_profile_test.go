package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestProfileForUserIncludesIdentitiesTeamsAndProjectAccess(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "profile.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	user := core.User{
		ID: "user-1", Username: "alex", DisplayName: "Alex Morgan",
		Email: "alex@example.com", SystemRole: core.UserRoleMember,
		State: core.UserStateActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := data.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateProject(ctx, core.Project{ID: "project-1", Name: "Checkout", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateTeam(ctx, core.Team{ID: "team-1", Name: "Platform", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := data.ReplaceTeamMembers(ctx, "team-1", []core.TeamMember{{
		TeamID: "team-1", UserID: user.ID, Role: core.TeamMemberRoleMember, CreatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	for _, assignment := range []core.RoleAssignment{
		{ID: "grant-user", PrincipalType: core.PrincipalUser, PrincipalID: user.ID, ScopeType: core.ScopeProject, ScopeID: "project-1", Role: core.RoleViewer, CreatedAt: now, UpdatedAt: now},
		{ID: "grant-team", PrincipalType: core.PrincipalTeam, PrincipalID: "team-1", ScopeType: core.ScopeProject, ScopeID: "project-1", Role: core.RoleOperator, CreatedAt: now, UpdatedAt: now},
	} {
		if err := data.UpsertRoleAssignment(ctx, assignment); err != nil {
			t.Fatal(err)
		}
	}
	provider := core.AuthProvider{
		ID: "provider-1", Name: "Company GitHub", Type: core.AuthProviderGitHub,
		BaseURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3",
		ClientID: "client-id", EncryptedClientSecret: "encrypted", Provisioning: core.AuthProvisionExisting,
		State: core.AuthProviderStateDisabled, CreatedAt: now, UpdatedAt: now,
	}
	if err := data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := data.UpsertExternalIdentity(ctx, core.ExternalIdentity{
		ProviderID: provider.ID, Subject: "42", UserID: user.ID,
		Login: "alex", Email: user.Email, LastLogin: now, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	profile, err := (&API{store: data}).profileForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(profile.Links) != 1 || profile.Links[0].Identity == nil || profile.Links[0].Available {
		t.Fatalf("expected the disabled linked identity to remain visible: %#v", profile.Links)
	}
	if len(profile.Teams) != 1 || profile.Teams[0].Name != "Platform" {
		t.Fatalf("unexpected teams: %#v", profile.Teams)
	}
	if len(profile.ProjectAccess) != 2 {
		t.Fatalf("expected direct and team access, got %#v", profile.ProjectAccess)
	}
	sources := map[string]string{}
	for _, grant := range profile.ProjectAccess {
		sources[grant.Source] = grant.Role
		if grant.ProjectName != "Checkout" {
			t.Fatalf("expected project name, got %#v", grant)
		}
	}
	if sources[core.PrincipalUser] != core.RoleViewer || sources[core.PrincipalTeam] != core.RoleOperator {
		t.Fatalf("unexpected project access: %#v", profile.ProjectAccess)
	}
}

package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestSQLiteAuthProviderAndIdentityRoundTrip(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	user := core.User{ID: "user-1", Username: "alex", DisplayName: "Alex", Email: "alex@example.com", PasswordHash: "", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	provider := core.AuthProvider{ID: "provider-1", Name: "Company GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", ClientID: "client", EncryptedClientSecret: "ciphertext", Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now}
	if err := data.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	stored, err := data.GetAuthProvider(ctx, provider.ID)
	if err != nil || !stored.ClientSecretConfigured {
		t.Fatalf("unexpected provider: %#v %v", stored, err)
	}
	identity := core.ExternalIdentity{ProviderID: provider.ID, Subject: "42", UserID: user.ID, Login: "alex-gh", Email: user.Email, LastLogin: now, CreatedAt: now}
	if err := data.UpsertExternalIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	linked, err := data.GetExternalIdentity(ctx, provider.ID, "42")
	if err != nil || linked.UserID != user.ID {
		t.Fatalf("unexpected identity: %#v %v", linked, err)
	}
	if items, err := data.FindExternalIdentitiesByLogin(ctx, "ALEX-GH"); err != nil || len(items) != 1 {
		t.Fatalf("identity lookup: %#v %v", items, err)
	}
	if items, err := data.ListExternalIdentities(ctx); err != nil || len(items) != 1 || items[0].ProviderID != provider.ID {
		t.Fatalf("identity list: %#v %v", items, err)
	}
	if err := data.DeleteAuthProvider(ctx, provider.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetExternalIdentity(ctx, provider.ID, "42"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected linked identity removal, got %v", err)
	}
}

func TestSQLiteMergeUsersMovesIdentityAndAccess(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "merge-users.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	source := core.User{ID: "source-user", Username: "alex-gh", DisplayName: "Alex Morgan", Email: "alex@example.com", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	target := core.User{ID: "target-user", Username: "alex", DisplayName: "Alex", PasswordHash: "password-hash", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	for _, user := range []core.User{source, target} {
		if err := data.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	provider := core.AuthProvider{ID: "provider-1", Name: "GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", ClientID: "client", EncryptedClientSecret: "ciphertext", Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now}
	if err := data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err := data.UpsertExternalIdentity(ctx, core.ExternalIdentity{ProviderID: provider.ID, Subject: "42", UserID: source.ID, Login: "alex", Email: source.Email, LastLogin: now, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	team := core.Team{ID: "team-1", Name: "Platform", CreatedAt: now, UpdatedAt: now}
	if err := data.CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	if err := data.ReplaceTeamMembers(ctx, team.ID, []core.TeamMember{{TeamID: team.ID, UserID: source.ID, Role: core.TeamMemberRoleMember, CreatedAt: now}}); err != nil {
		t.Fatal(err)
	}
	project := core.Project{ID: "project-1", Name: "Checkout", CreatedAt: now}
	if err := data.CreateProject(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := data.UpsertRoleAssignment(ctx, core.RoleAssignment{ID: "assignment-1", PrincipalType: core.PrincipalUser, PrincipalID: source.ID, ScopeType: core.ScopeProject, ScopeID: project.ID, Role: core.RoleDeployer, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	if err := data.MergeUsers(ctx, source.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := data.GetUser(ctx, source.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected source user to be removed, got %v", err)
	}
	identity, err := data.GetExternalIdentity(ctx, provider.ID, "42")
	if err != nil || identity.UserID != target.ID {
		t.Fatalf("identity was not moved: %#v %v", identity, err)
	}
	members, err := data.ListTeamMembers(ctx)
	if err != nil || len(members) != 1 || members[0].UserID != target.ID {
		t.Fatalf("team membership was not moved: %#v %v", members, err)
	}
	assignments, err := data.ListRoleAssignments(ctx)
	if err != nil || len(assignments) != 1 || assignments[0].PrincipalID != target.ID {
		t.Fatalf("direct grant was not moved: %#v %v", assignments, err)
	}
}

func TestSQLiteMergeUsersRejectsSharedAuthenticationType(t *testing.T) {
	ctx := context.Background()
	data, err := Open(ctx, filepath.Join(t.TempDir(), "merge-auth-conflict.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	users := []core.User{
		{ID: "local-source", Username: "local-source", DisplayName: "Local source", PasswordHash: "source-hash", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now},
		{ID: "local-target", Username: "local-target", DisplayName: "Local target", PasswordHash: "target-hash", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now},
		{ID: "github-source", Username: "github-source", DisplayName: "GitHub source", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now},
		{ID: "github-target", Username: "github-target", DisplayName: "GitHub target", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now},
	}
	for _, user := range users {
		if err := data.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	providers := []core.AuthProvider{
		{ID: "github-public", Name: "GitHub.com", Type: core.AuthProviderGitHub, BaseURL: "https://github.com", APIURL: "https://api.github.com", ClientID: "public-client", EncryptedClientSecret: "ciphertext", Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now},
		{ID: "github-enterprise", Name: "Company GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", ClientID: "enterprise-client", EncryptedClientSecret: "ciphertext", Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now},
	}
	for _, provider := range providers {
		if err := data.CreateAuthProvider(ctx, provider); err != nil {
			t.Fatal(err)
		}
	}
	identities := []core.ExternalIdentity{
		{ProviderID: providers[0].ID, Subject: "42", UserID: "github-source", Login: "source", LastLogin: now, CreatedAt: now},
		{ProviderID: providers[1].ID, Subject: "84", UserID: "github-target", Login: "target", LastLogin: now, CreatedAt: now},
	}
	for _, identity := range identities {
		if err := data.UpsertExternalIdentity(ctx, identity); err != nil {
			t.Fatal(err)
		}
	}

	if err := data.MergeUsers(ctx, "local-source", "local-target"); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("expected local authentication conflict, got %v", err)
	}
	if err := data.MergeUsers(ctx, "github-source", "github-target"); !errors.Is(err, ErrIdentityConflict) {
		t.Fatalf("expected GitHub authentication conflict, got %v", err)
	}
}

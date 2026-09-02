package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
	"github.com/doout/dispatch/internal/store"
)

func TestExternalAccountRequiresOwnerApproval(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "oauth.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	provider := core.AuthProvider{
		ID:                    "provider-1",
		Name:                  "Company GitHub",
		Type:                  core.AuthProviderGitHub,
		BaseURL:               "https://github.example.com",
		APIURL:                "https://github.example.com/api/v3",
		ClientID:              "client-id",
		EncryptedClientSecret: "ciphertext",
		Provisioning:          core.AuthProvisionApproval,
		State:                 core.AuthProviderStateReady,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if err := data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}

	controller := &API{store: data}
	profile := githubOAuthProfile{
		Subject:     "42",
		Login:       "alex",
		DisplayName: "Alex Morgan",
		Email:       "alex@example.com",
	}
	if _, err := controller.resolveExternalUser(ctx, provider, profile); !errors.Is(err, errExternalApprovalPending) {
		t.Fatalf("expected approval to be required, got %v", err)
	}

	users, err := data.ListUsers(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("expected one pending user, got %#v %v", users, err)
	}
	if users[0].State != core.UserStatePending || users[0].SystemRole != core.UserRoleMember || users[0].PasswordHash != "" {
		t.Fatalf("unexpected external user: %#v", users[0])
	}
	if _, err := data.GetExternalIdentity(ctx, provider.ID, profile.Subject); err != nil {
		t.Fatalf("expected the external identity to be linked: %v", err)
	}
	if _, err := controller.resolveExternalUser(ctx, provider, profile); !errors.Is(err, errExternalApprovalPending) {
		t.Fatalf("expected a linked pending identity to remain blocked, got %v", err)
	}

	users[0].State = core.UserStateActive
	users[0].UpdatedAt = time.Now().UTC()
	if err := data.UpdateUser(ctx, users[0]); err != nil {
		t.Fatal(err)
	}
	approved, err := controller.resolveExternalUser(ctx, provider, profile)
	if err != nil || approved.ID != users[0].ID {
		t.Fatalf("expected approved sign-in, got %#v %v", approved, err)
	}
}

func TestUnknownExternalAccountIsDeniedWhenEnrollmentIsClosed(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "oauth-denied.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	provider := core.AuthProvider{
		ID:           "provider-1",
		Provisioning: core.AuthProvisionExisting,
	}
	controller := &API{store: data}
	profile := githubOAuthProfile{Subject: "42", Login: "alex", DisplayName: "Alex Morgan", Email: "alex@example.com"}
	if _, err := controller.resolveExternalUser(ctx, provider, profile); !errors.Is(err, errExternalAccessDenied) {
		t.Fatalf("expected access to be denied, got %v", err)
	}
	if users, err := data.ListUsers(ctx); err != nil || len(users) != 0 {
		t.Fatalf("unexpected user creation: %#v %v", users, err)
	}
}

func TestLinkExternalIdentityRejectsAccountConflicts(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "oauth-link.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	first := core.User{ID: "user-1", Username: "alex", DisplayName: "Alex", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	second := core.User{ID: "user-2", Username: "sam", DisplayName: "Sam", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	for _, user := range []core.User{first, second} {
		if err := data.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	provider := core.AuthProvider{ID: "provider-1", Name: "GitHub", Type: core.AuthProviderGitHub, BaseURL: "https://github.example.com", APIURL: "https://github.example.com/api/v3", ClientID: "client", EncryptedClientSecret: "ciphertext", Provisioning: core.AuthProvisionExisting, State: core.AuthProviderStateReady, CreatedAt: now, UpdatedAt: now}
	if err := data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	controller := &API{store: data}
	profile := githubOAuthProfile{Subject: "42", Login: "alex-gh", Email: "alex@example.com"}

	if err := controller.linkExternalIdentity(ctx, provider, profile, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := controller.linkExternalIdentity(ctx, provider, profile, second.ID); !errors.Is(err, errExternalIdentityOwned) {
		t.Fatalf("expected account ownership conflict, got %v", err)
	}
	otherProfile := githubOAuthProfile{Subject: "73", Login: "alex-work", Email: "alex@work.example"}
	if err := controller.linkExternalIdentity(ctx, provider, otherProfile, first.ID); !errors.Is(err, errExternalProviderUsed) {
		t.Fatalf("expected provider conflict, got %v", err)
	}
}

func TestMergedExternalIdentitySignsInAsRemainingUser(t *testing.T) {
	ctx := context.Background()
	data, err := store.Open(ctx, filepath.Join(t.TempDir(), "oauth-merged-user.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	if err := data.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	provider := core.AuthProvider{
		ID:           "provider-1",
		Name:         "GitHub",
		Type:         core.AuthProviderGitHub,
		Provisioning: core.AuthProvisionExisting,
		State:        core.AuthProviderStateReady,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := data.CreateAuthProvider(ctx, provider); err != nil {
		t.Fatal(err)
	}
	source := core.User{ID: "github-user", Username: "alex-gh", DisplayName: "Alex GitHub", Email: "alex@example.com", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	target := core.User{ID: "local-user", Username: "alex", DisplayName: "Alex", PasswordHash: "password-hash", SystemRole: core.UserRoleMember, State: core.UserStateActive, CreatedAt: now, UpdatedAt: now}
	for _, user := range []core.User{source, target} {
		if err := data.CreateUser(ctx, user); err != nil {
			t.Fatal(err)
		}
	}
	identity := core.ExternalIdentity{ProviderID: provider.ID, Subject: "42", UserID: source.ID, Login: "alex-gh", Email: source.Email, LastLogin: now, CreatedAt: now}
	if err := data.UpsertExternalIdentity(ctx, identity); err != nil {
		t.Fatal(err)
	}
	if err := data.MergeUsers(ctx, source.ID, target.ID); err != nil {
		t.Fatal(err)
	}

	controller := &API{store: data}
	profile := githubOAuthProfile{Subject: "42", Login: "alex-gh", DisplayName: "Alex GitHub", Email: "alex@example.com"}
	user, err := controller.resolveExternalUser(ctx, provider, profile)
	if err != nil {
		t.Fatal(err)
	}
	if user.ID != target.ID {
		t.Fatalf("expected merged GitHub identity to sign in as %q, got %#v", target.ID, user)
	}
	linked, err := data.GetExternalIdentity(ctx, provider.ID, profile.Subject)
	if err != nil || linked.UserID != target.ID {
		t.Fatalf("expected identity to remain linked to the surviving user, got %#v %v", linked, err)
	}
}

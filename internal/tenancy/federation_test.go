package tenancy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestExternalUsersRequireExplicitLink(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := context.Background()
			if err := c.CreateUser(ctx, User{ID: "admin", Email: "admin@example.test", Name: "Admin", EmailVerified: true, PlatformAdmin: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := c.ResolveExternalUser(ctx, ExternalLogin{Provider: "workos", Subject: "new-subject", Email: "admin@example.test", Name: "Admin", LinkUserID: "", LinkVersion: 1}); !errors.Is(err, ErrConflict) {
				t.Fatalf("email silently linked: %v", err)
			}
			if _, err := c.ExternalUser(ctx, "workos", "new-subject"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("conflict persisted a binding: %v", err)
			}
			u, err := c.ResolveExternalUser(ctx, ExternalLogin{Provider: "workos", Subject: "new-subject", Email: "other@example.test", Name: "Other", LinkUserID: "admin", LinkVersion: 1})
			if err != nil || u.ID != "admin" || !u.PlatformAdmin {
				t.Fatalf("explicit link lost account: %+v %v", u, err)
			}
			if _, err = c.ResolveExternalUser(ctx, ExternalLogin{Provider: "workos", Subject: "new-subject", Email: "other@example.test", Name: "Other", LinkUserID: "someone-else", LinkVersion: 1}); !errors.Is(err, ErrDenied) {
				t.Fatalf("rebound identity: %v", err)
			}
			u, err = c.ResolveExternalUser(ctx, ExternalLogin{Provider: "workos", Subject: "member-subject", Email: "member@example.test", Name: "Member", LinkUserID: "", LinkVersion: 1})
			if err != nil || u.PlatformAdmin || !u.EmailVerified || u.PasswordHash != "" {
				t.Fatalf("new user: %+v %v", u, err)
			}
			if err = c.SetUserState(ctx, u.ID, StateDisabled); err != nil {
				t.Fatal(err)
			}
			if _, err = c.ResolveExternalUser(ctx, ExternalLogin{Provider: "workos", Subject: "member-subject", Email: "changed@example.test", Name: "Changed", LinkUserID: "", LinkVersion: 1}); !errors.Is(err, ErrDenied) {
				t.Fatalf("disabled user signed in: %v", err)
			}
		})
	}
}

func TestLoginAttemptIsBrowserBoundSingleUseAndExpires(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) { testLoginAttemptBoundState(t, postgres) })
	}
}
func testLoginAttemptBoundState(t *testing.T, postgres bool) {
	c := testCatalog(t, postgres)
	ctx := context.Background()
	continuation := json.RawMessage(`{"step":"verify-email","pendingToken":"private-provider-token","deadline":"2026-10-12T01:00:00Z"}`)
	token, err := c.CreateLoginAttempt(ctx, "browser", LoginAttempt{Verifier: "verifier", ReturnTo: "/", Continuation: continuation, InvitationToken: "private-invitation"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ConsumeLoginAttempt(ctx, token, "other-browser"); !errors.Is(err, ErrExpired) {
		t.Fatalf("wrong browser: %v", err)
	}
	a, err := c.ConsumeLoginAttempt(ctx, token, "browser")
	if err != nil || a.Verifier != "verifier" || string(a.Continuation) != string(continuation) || a.InvitationToken != "private-invitation" {
		t.Fatalf("consume: %+v %v", a, err)
	}
	if _, err = c.ConsumeLoginAttempt(ctx, token, "browser"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replayed attempt: %v", err)
	}
	token, err = c.CreateLoginAttempt(ctx, "browser", LoginAttempt{})
	if err != nil {
		t.Fatal(err)
	}
	now := c.now()
	c.now = func() time.Time { return now.Add(11 * time.Minute) }
	if _, err = c.ConsumeLoginAttempt(ctx, token, "browser"); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired attempt: %v", err)
	}
}

func TestWorkOSOriginSurvivesTenantHandoff(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	if err := c.CreateUser(ctx, User{ID: "owner", Name: "Owner", Email: "owner@example.test", EmailVerified: true, PlatformAdmin: true}); err != nil {
		t.Fatal(err)
	}
	tenant, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "team", Name: "Team", InitialOwnerID: "owner", CreatorID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SetTenantState(ctx, tenant.ID, StateActive); err != nil {
		t.Fatal(err)
	}
	origin := LoginOrigin{Provider: "workos", Subject: "user_1", SessionID: "session_1"}
	platform, err := c.CreateExternalSession(ctx, "owner", AudiencePlatform, time.Hour, origin)
	if err != nil {
		t.Fatal(err)
	}
	verifier, _ := randomToken()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	code, err := c.CreateHandoff(ctx, HandoffRequest{UserID: "owner", TenantID: tenant.ID, Origin: "https://team.example.test", CodeChallenge: challenge})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.CopyLoginOrigin(ctx, platform, code, time.Minute); err != nil {
		t.Fatal(err)
	}
	token, err := c.ExchangeHandoff(ctx, HandoffExchange{Code: code, TenantID: tenant.ID, Origin: "https://team.example.test", CodeVerifier: verifier})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.LoginOrigin(ctx, token)
	if err != nil || got != origin {
		t.Fatalf("lost upstream session: %+v %v", got, err)
	}
	if _, err = c.AuthenticateSession(ctx, token, AudiencePlatform); !errors.Is(err, ErrExpired) {
		t.Fatalf("tenant session used on platform: %v", err)
	}
}

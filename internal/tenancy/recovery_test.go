package tenancy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOfflineAdminRecoveryCannotGrantAccess(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	seedUsers(t, c)

	tenant := seedTenant(t, c, "alpha")
	for _, email := range []string{"owner@example.test", "missing@example.test"} {
		if err := c.RecoverPlatformAdminPassword(ctx, email, "recovered-password-long"); !errors.Is(err, ErrDenied) {
			t.Fatal("nonadmin recovered", err)
		}
	}
	session, err := c.CreateSession(ctx, "platform", AudiencePlatform, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.RecoverPlatformAdminPassword(ctx, "platform@example.test", "recovered-password-long"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthenticateSession(ctx, session, AudiencePlatform); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err = c.AuthenticatePassword(ctx, "platform@example.test", "recovered-password-long"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Membership(ctx, tenant.ID, "platform"); !errors.Is(err, ErrNotFound) {
		t.Fatal("admin gained membership", err)
	}
}

func TestRecoveryCannotEnableOrPromoteAccounts(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	for _, u := range []User{{ID: "disabled", Email: "disabled@example.test", Name: "Disabled", State: StateDisabled, PlatformAdmin: true, EmailVerified: true}, {ID: "pending", Email: "pending@example.test", Name: "Pending", State: StatePending, PlatformAdmin: true}, {ID: "unverified", Email: "unverified@example.test", Name: "Unverified", PlatformAdmin: true}} {
		if err := c.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := c.RecoverPlatformAdminPassword(ctx, u.Email, "recovered-password-long"); !errors.Is(err, ErrDenied) {
			t.Fatal("ineligible account recovered", err)
		}
		current, err := c.User(ctx, u.ID)
		if err != nil || current.Version != 1 || current.PasswordHash != "" {
			t.Fatal("account changed", current, err)
		}
	}
}

func TestOfflineProvisioningRequiresAdministratorAndPreservesExistingIdentity(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := context.Background()
			if err := c.ProvisionUser(ctx, "owner@example.test", "Owner", "initial-password-long"); !errors.Is(err, ErrDenied) {
				t.Fatal("provisioned first account", err)
			}
			if err := c.BootstrapUser(ctx, User{Email: "admin@example.test", Name: "Admin"}); err != nil {
				t.Fatal(err)
			}
			if err := c.ProvisionUser(ctx, " OWNER@example.test ", "Owner", "initial-password-long"); err != nil {
				t.Fatal(err)
			}
			user, err := c.AuthenticatePassword(ctx, "owner@example.test", "initial-password-long")
			if err != nil || user.PlatformAdmin || !user.EmailVerified {
				t.Fatal(user, err)
			}
			admin, err := c.UserByEmail(ctx, "admin@example.test")
			if err != nil {
				t.Fatal(err)
			}
			tenant, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "alpha", Name: "Alpha", CreatorID: admin.ID, InitialOwnerID: user.ID})
			if err != nil {
				t.Fatal("verified offline user could not become tenant owner", err)
			}
			membership, err := c.Membership(ctx, tenant.ID, user.ID)
			if err != nil || membership.Role != RoleOwner || membership.State != StateActive {
				t.Fatal("tenant owner was not active", membership, err)
			}
			if _, err := c.Membership(ctx, tenant.ID, admin.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal("tenant creation granted platform administrator access", err)
			}
			if err := c.ProvisionUser(ctx, "owner@example.test", "Attacker", "replacement-password-long"); !errors.Is(err, ErrConflict) {
				t.Fatal("duplicate account changed", err)
			}
			if _, err := c.AuthenticatePassword(ctx, "owner@example.test", "initial-password-long"); err != nil {
				t.Fatal("existing password changed", err)
			}
			if err := c.ProvisionUser(ctx, "third@example.test", "Third", "short"); !errors.Is(err, ErrInvalid) {
				t.Fatal("short password accepted", err)
			}
		})
	}
}

func TestAdminRecoveryRevokesTenantSessionAndHandoffWithoutChangingMembership(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := context.Background()
			seedUsers(t, c)
			tenant := seedTenant(t, c, "alpha")
			if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "platform", Role: RoleMember}); err != nil {
				t.Fatal(err)
			}
			session, err := c.CreateSession(ctx, "platform", TenantAudience(tenant.ID), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			verifier := strings.Repeat("x", 43)
			digest := sha256.Sum256([]byte(verifier))
			handoff, err := c.CreateHandoff(ctx, HandoffRequest{UserID: "platform", TenantID: tenant.ID, Origin: "https://alpha.example.test", CodeChallenge: base64.RawURLEncoding.EncodeToString(digest[:])})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.RecoverPlatformAdminPassword(ctx, "platform@example.test", "recovered-password-long"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.AuthenticateSession(ctx, session, TenantAudience(tenant.ID)); !errors.Is(err, ErrExpired) {
				t.Fatal("old tenant session remains valid", err)
			}
			if _, err := c.ExchangeHandoff(ctx, HandoffExchange{Code: handoff, TenantID: tenant.ID, Origin: "https://alpha.example.test", CodeVerifier: verifier}); !errors.Is(err, ErrExpired) {
				t.Fatal("old handoff remains valid", err)
			}
			membership, err := c.Membership(ctx, tenant.ID, "platform")
			if err != nil || membership.Role != RoleMember || membership.State != StateActive {
				t.Fatal("membership changed", membership, err)
			}
		})
	}
}

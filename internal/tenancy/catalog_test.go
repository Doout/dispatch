package tenancy

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/crypto/bcrypt"
)

func testCatalog(t *testing.T, postgres bool) *Catalog {
	t.Helper()
	ctx := context.Background()
	dsn := filepath.Join(t.TempDir(), "catalog.db")
	if postgres {
		base := os.Getenv("DISPATCH_TEST_POSTGRES_URL")
		if base == "" {
			base = os.Getenv("DISPATCH_STORE_POSTGRES_URL")
		}
		if base == "" {
			t.Skip("disposable PostgreSQL URL is not configured")
		}
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("pgx", base)
		if err != nil {
			t.Fatal(err)
		}
		schema := "tenancy_test_" + strings.ToLower(ulid.Make().String())
		if _, err = db.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			defer db.Close()
			if _, err := db.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
				t.Error(err)
			}
		})
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	c, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err = c.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = c.Migrate(ctx); err != nil {
		t.Fatal("repeat migration", err)
	}
	return c
}
func seedUsers(t *testing.T, c *Catalog) {
	t.Helper()
	for _, u := range []User{{ID: "platform", Email: "platform@example.test", Name: "Platform", PlatformAdmin: true, EmailVerified: true}, {ID: "owner", Email: "owner@example.test", Name: "Owner", EmailVerified: true}, {ID: "member", Email: "member@example.test", Name: "Member", EmailVerified: true}, {ID: "outsider", Email: "outsider@example.test", Name: "Outsider", EmailVerified: true}} {
		if err := c.CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
}
func seedTenant(t *testing.T, c *Catalog, slug string) Tenant {
	t.Helper()
	v, err := c.CreateTenant(context.Background(), CreateTenantInput{Slug: slug, Name: slug, CreatorID: "platform", InitialOwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.SetTenantState(context.Background(), v.ID, "active"); err != nil {
		t.Fatal(err)
	}
	v.State = "active"
	return v
}

func TestCatalogTenantBoundaries(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := context.Background()
			seedUsers(t, c)
			tenant := seedTenant(t, c, "agentops")
			if _, err := c.Membership(ctx, tenant.ID, "platform"); !errors.Is(err, ErrNotFound) {
				t.Fatal("creator acquired tenant membership", err)
			}
			if _, err := c.CreateSession(ctx, "platform", TenantAudience(tenant.ID), time.Hour); !errors.Is(err, ErrDenied) {
				t.Fatal("platform role bypassed membership", err)
			}
			if err := c.SetMembership(ctx, "platform", Membership{TenantID: tenant.ID, UserID: "platform", Role: RoleOwner}); !errors.Is(err, ErrDenied) {
				t.Fatal("platform self-enrollment", err)
			}
			if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "member", Role: RoleMember}); err != nil {
				t.Fatal(err)
			}
			if err := c.SetMembership(ctx, "member", Membership{TenantID: tenant.ID, UserID: "member", Role: RoleOwner}); !errors.Is(err, ErrDenied) {
				t.Fatal("member elevated itself", err)
			}
			if err := c.DeleteMembership(ctx, "owner", tenant.ID, "owner"); !errors.Is(err, ErrLastOwner) {
				t.Fatal("removed last owner", err)
			}
			if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "owner", Role: RoleMember}); !errors.Is(err, ErrLastOwner) {
				t.Fatal("demoted last owner", err)
			}
			if err := c.SetUserState(ctx, "owner", StateDisabled); !errors.Is(err, ErrLastOwner) {
				t.Fatal("disabled last owner", err)
			}
			if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "member", Role: RoleOwner}); err != nil {
				t.Fatal(err)
			}
			var success atomic.Int32
			var wg sync.WaitGroup
			for _, id := range []string{"owner", "member"} {
				wg.Add(1)
				go func(id string) {
					defer wg.Done()
					if err := c.DeleteMembership(ctx, id, tenant.ID, id); err == nil {
						success.Add(1)
					} else if !errors.Is(err, ErrLastOwner) {
						t.Error(err)
					}
				}(id)
			}
			wg.Wait()
			if success.Load() != 1 {
				t.Fatal("last-owner race", success.Load())
			}
			if _, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "other", Name: "Other", CreatorID: "outsider", InitialOwnerID: "outsider"}); !errors.Is(err, ErrDenied) {
				t.Fatal("non-admin created tenant", err)
			}
			if _, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "agentops", Name: "Other", CreatorID: "platform", InitialOwnerID: "owner"}); !errors.Is(err, ErrConflict) {
				t.Fatal("duplicate slug", err)
			}
			if _, err := c.ListTenantMembers(ctx, "platform", tenant.ID); !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrDenied) {
				t.Fatal("platform read members", err)
			}
		})
	}
}

func TestCatalogInvitationsRequireVerifiedRecipient(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	seedUsers(t, c)
	if err := c.CreateUser(ctx, User{ID: "invited", Email: "Invited@example.test", Name: "Invited"}); err != nil {
		t.Fatal(err)
	}
	tenant, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "invited", Name: "Invited", CreatorID: "platform", InvitationEmail: "INVITED@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"platform", "outsider", "invited"} {
		if err = c.AcceptInvitation(ctx, id, tenant.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("unverified/wrong identity accepted", id, err)
		}
	}
	verification, err := c.CreateEmailVerification(ctx, "invited")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.VerifyEmail(ctx, verification); err != nil {
		t.Fatal(err)
	}
	if _, err = c.VerifyEmail(ctx, verification); !errors.Is(err, ErrExpired) {
		t.Fatal("verification replay", err)
	}
	if err = c.AcceptInvitation(ctx, "invited", tenant.ID); err != nil {
		t.Fatal(err)
	}
	if err = c.AcceptInvitation(ctx, "invited", tenant.ID); !errors.Is(err, ErrDenied) {
		t.Fatal("invitation replay", err)
	}
	if _, err = c.Membership(ctx, tenant.ID, "platform"); !errors.Is(err, ErrNotFound) {
		t.Fatal("creator joined on invitation acceptance", err)
	}
}

func TestSessionsHandoffsAndRevocation(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := context.Background()
			seedUsers(t, c)
			a := seedTenant(t, c, "alpha")
			b := seedTenant(t, c, "bravo")
			for _, tenant := range []Tenant{a, b} {
				if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "member", Role: RoleMember}); err != nil {
					t.Fatal(err)
				}
			}
			token, err := c.CreateSession(ctx, "member", TenantAudience(a.ID), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.AuthenticateSession(ctx, token, TenantAudience(a.ID)); err != nil {
				t.Fatal(err)
			}
			for _, audience := range []string{AudiencePlatform, TenantAudience(b.ID)} {
				if _, err = c.AuthenticateSession(ctx, token, audience); !errors.Is(err, ErrExpired) {
					t.Fatal("session crossed audience", err)
				}
			}
			central, err := c.CreateSession(ctx, "member", AudiencePlatform, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.AuthenticateSession(ctx, central, TenantAudience(a.ID)); !errors.Is(err, ErrExpired) {
				t.Fatal("central session used directly on tenant", err)
			}
			verifier := strings.Repeat("a", 43)
			sum := sha256.Sum256([]byte(verifier))
			challenge := base64.RawURLEncoding.EncodeToString(sum[:])
			request := HandoffRequest{UserID: "member", TenantID: a.ID, Origin: "https://alpha.dispatch.example.test", CodeChallenge: challenge}
			code, err := c.CreateHandoff(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			exchange := HandoffExchange{Code: code, TenantID: a.ID, Origin: request.Origin, CodeVerifier: verifier}
			for _, wrong := range []HandoffExchange{{Code: code, TenantID: b.ID, Origin: request.Origin, CodeVerifier: verifier}, {Code: code, TenantID: a.ID, Origin: "https://bravo.dispatch.example.test", CodeVerifier: verifier}, {Code: code, TenantID: a.ID, Origin: request.Origin, CodeVerifier: strings.Repeat("b", 43)}} {
				if _, err = c.ExchangeHandoff(ctx, wrong); !errors.Is(err, ErrExpired) {
					t.Fatal("accepted wrong handoff target/verifier", err)
				}
			}
			var successes atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 6; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if _, err := c.ExchangeHandoff(ctx, exchange); err == nil {
						successes.Add(1)
					} else if !errors.Is(err, ErrExpired) {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
			if successes.Load() != 1 {
				t.Fatal("handoff redeemed multiple times", successes.Load())
			}
			code, err = c.CreateHandoff(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			exchange.Code = code
			if err = c.DeleteMembership(ctx, "owner", a.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err = c.SetMembership(ctx, "owner", Membership{TenantID: a.ID, UserID: "member", Role: RoleMember}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.AuthenticateSession(ctx, token, TenantAudience(a.ID)); !errors.Is(err, ErrExpired) {
				t.Fatal("remove/re-add revived session", err)
			}
			if _, err = c.ExchangeHandoff(ctx, exchange); !errors.Is(err, ErrExpired) {
				t.Fatal("remove/re-add revived handoff", err)
			}
			code, err = c.CreateHandoff(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			exchange.Code = code
			now := time.Now()
			c.now = func() time.Time { return now.Add(2 * time.Minute) }
			if _, err = c.ExchangeHandoff(ctx, exchange); !errors.Is(err, ErrExpired) {
				t.Fatal("expired handoff accepted", err)
			}
			if err = c.RevokeUserSessions(ctx, "member"); err != nil {
				t.Fatal(err)
			}
			if _, err = c.AuthenticateSession(ctx, central, AudiencePlatform); !errors.Is(err, ErrExpired) {
				t.Fatal("global revocation failed", err)
			}
		})
	}
}

func TestPasswordChangeRevokesSessions(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: "person", Email: "person@example.test", Name: "Person", PasswordHash: string(hash)}
	if err = c.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	token, err := c.CreateSession(ctx, u.ID, AudiencePlatform, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.UpdateOwnPassword(ctx, u.ID, "wrong", "replacement-password"); !errors.Is(err, ErrDenied) {
		t.Fatal(err)
	}
	if err = c.UpdateOwnPassword(ctx, u.ID, "original-password-123", "replacement-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthenticateSession(ctx, token, AudiencePlatform); !errors.Is(err, ErrExpired) {
		t.Fatal("old session survived password change", err)
	}
	if _, err = c.AuthenticatePassword(ctx, u.Email, "replacement-password"); err != nil {
		t.Fatal(err)
	}
}

func TestRegistrationRecipientSetsFirstPassword(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	if err := c.CreateUser(ctx, User{ID: "recipient", Email: "recipient@example.test", Name: "Recipient", State: StatePending}); err != nil {
		t.Fatal(err)
	}
	token, err := c.CreateEmailVerification(ctx, "recipient")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthenticatePassword(ctx, "recipient@example.test", "attacker-password"); !errors.Is(err, ErrDenied) {
		t.Fatal("pending account authenticated", err)
	}
	user, err := c.CompleteRegistration(ctx, token, "Recipient name", "recipient-chosen-password")
	if err != nil {
		t.Fatal(err)
	}
	if !user.EmailVerified || user.State != StateActive {
		t.Fatal("registration incomplete")
	}
	if _, err = c.AuthenticatePassword(ctx, user.Email, "recipient-chosen-password"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CompleteRegistration(ctx, token, "Attacker", "replacement-password"); !errors.Is(err, ErrExpired) {
		t.Fatal("replay replaced password", err)
	}
}

func TestDNSRecordOwnershipAndDeletionGeneration(t *testing.T) {
	c := testCatalog(t, false)
	ctx := context.Background()
	seedUsers(t, c)
	a := seedTenant(t, c, "alpha")
	b := seedTenant(t, c, "bravo")
	first, err := c.SaveZoneRecord(ctx, ZoneRecord{ID: "challenge-a", OwnerID: "cert-a", TenantID: a.ID, Name: "_acme-challenge.alpha.example.test", Type: "TXT", Values: []string{"a"}, TTL: 60})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.SaveZoneRecord(ctx, ZoneRecord{ID: "challenge-b", OwnerID: "cert-b", TenantID: a.ID, Name: first.Name, Type: "TXT", Values: []string{"b"}, TTL: 60})
	if err != nil {
		t.Fatal(err)
	}
	wrong := first
	wrong.TenantID = b.ID
	if _, err = c.SaveZoneRecord(ctx, wrong); !errors.Is(err, ErrDenied) {
		t.Fatal("changed tenant ownership", err)
	}
	wrong = first
	wrong.OwnerID = "cert-b"
	if _, err = c.SaveZoneRecord(ctx, wrong); !errors.Is(err, ErrDenied) {
		t.Fatal("changed record ownership", err)
	}
	wrong = first
	wrong.Generation = 0
	if _, err = c.SaveZoneRecord(ctx, wrong); !errors.Is(err, ErrConflict) {
		t.Fatal("stale write", err)
	}
	if err = c.DeleteZoneRecord(ctx, first.ID, second.OwnerID, first.Generation); !errors.Is(err, ErrDenied) {
		t.Fatal("cross-owner cleanup", err)
	}
	if err = c.DeleteZoneRecord(ctx, first.ID, first.OwnerID, first.Generation); err != nil {
		t.Fatal(err)
	}
	generation, records, err := c.ZoneRecords(ctx)
	if err != nil || len(records) != 1 || records[0].ID != second.ID || generation != 3 {
		t.Fatal("concurrent challenge lost", generation, records, err)
	}
	if err = c.DeleteZoneRecord(ctx, second.ID, second.OwnerID, second.Generation); err != nil {
		t.Fatal(err)
	}
	generation, records, err = c.ZoneRecords(ctx)
	if err != nil || len(records) != 0 || generation != 4 {
		t.Fatal("empty zone generation", generation, records, err)
	}
}

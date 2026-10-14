package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRemoveInactiveInstanceKeepsRecoveryMetadata(t *testing.T) {
	for _, pg := range []bool{false, true} {
		for _, missingConnection := range []bool{false, true} {
			t.Run(fmt.Sprintf("postgres=%t/missing-connection=%t", pg, missingConnection), func(t *testing.T) {
				c := testCatalog(t, pg)
				seedUsers(t, c)
				ctx := context.Background()
				instance := seedTenant(t, c, "old-instance")
				instance, err := c.Tenant(ctx, instance.ID)
				if err != nil {
					t.Fatal(err)
				}
				if missingConnection {
					if _, err := c.db.ExecContext(ctx, c.q(`DELETE FROM instance_connections WHERE tenant_id=?`), instance.ID); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := c.DeleteInactiveInstance(ctx, "platform", instance.ID, instance.Slug, ""); !errors.Is(err, ErrDenied) {
					t.Fatal("platform admin removed foreign instance", err)
				}
				if _, err := c.DeleteConnectedInstance(ctx, "owner", instance.ID, instance.Slug, ""); !errors.Is(err, ErrDenied) {
					t.Fatal("ordinary removal accepted embedded data", err)
				}
				session, err := c.CreateSession(ctx, "owner", TenantAudience(instance.ID), time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = c.DeleteInactiveInstance(ctx, "owner", instance.ID, instance.Slug, ""); err != nil {
					t.Fatal(err)
				}
				if _, err = c.Tenant(ctx, instance.ID); !errors.Is(err, ErrNotFound) {
					t.Fatal("instance remains visible", err)
				}
				if _, err = c.AuthenticateSession(ctx, session, TenantAudience(instance.ID)); err == nil {
					t.Fatal("removed session accepted")
				}
				var raw, owner string
				if err = c.db.QueryRowContext(ctx, c.q(`SELECT metadata_json,owner_id FROM retired_instance_data WHERE tenant_id=?`), instance.ID).Scan(&raw, &owner); err != nil {
					t.Fatal(err)
				}
				var saved Tenant
				if err = json.Unmarshal([]byte(raw), &saved); err != nil || saved != instance || owner != "owner" {
					t.Fatal("recovery identity lost", saved, owner, err)
				}
			})
		}
	}
}

func TestDeleteConnectedInstanceRevokesAccessAndMetadata(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[pg], func(t *testing.T) {
			c := testCatalog(t, pg)
			seedUsers(t, c)
			ctx := context.Background()
			instance, err := c.CreateOwnConnectedTenant(ctx, "owner", "example", "Example")
			if err != nil {
				t.Fatal(err)
			}
			other, err := c.CreateOwnConnectedTenant(ctx, "owner", "other", "Other")
			if err != nil {
				t.Fatal(err)
			}
			if err = c.SetMembership(ctx, "owner", Membership{TenantID: instance.ID, UserID: "member", Role: RoleAdmin}); err != nil {
				t.Fatal(err)
			}
			enrollment, _, err := c.CreateInstanceEnrollment(ctx, "owner", instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			grant, credential, err := c.EnrollInstance(ctx, enrollment, "server", "http://192.168.2.14:8080", true)
			if err != nil {
				t.Fatal(err)
			}
			platformSession, err := c.CreateSession(ctx, "owner", AudiencePlatform, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			session, err := c.CreateExternalSession(ctx, "owner", TenantAudience(instance.ID), time.Hour, LoginOrigin{Provider: "workos", Subject: "user_owner", SessionID: "session_owner"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.CreateHandoff(ctx, HandoffRequest{UserID: "owner", TenantID: instance.ID, Origin: "https://example.doodship.test", CodeChallenge: strings.Repeat("A", 43)}); err != nil {
				t.Fatal(err)
			}
			if err = c.BindWorkOSOrganization(ctx, instance.ID, "org_example"); err != nil {
				t.Fatal(err)
			}
			if err = c.ReserveWorkloadHost(ctx, WorkloadHost{TenantID: instance.ID, Hostname: "p1-id-example.doodship.test", AppID: "app", ProjectID: "project", ServerID: "worker"}); err != nil {
				t.Fatal(err)
			}
			if err = c.SaveUsage(ctx, Usage{TenantID: instance.ID, PeriodStart: c.now().Add(-time.Hour), PeriodEnd: c.now()}); err != nil {
				t.Fatal(err)
			}
			record, err := c.SaveZoneRecord(ctx, ZoneRecord{ID: "example-dns", OwnerID: instance.ID, TenantID: instance.ID, Name: "example.doodship.test", Type: "A", Values: []string{"192.0.2.10"}, TTL: 60})
			if err != nil {
				t.Fatal(err)
			}
			for _, actor := range []string{"platform", "member", "outsider"} {
				if _, err = c.DeleteConnectedInstance(ctx, actor, instance.ID, instance.Slug, "doodship.test:"+instance.ID); !errors.Is(err, ErrDenied) {
					t.Fatalf("%s could delete: %v", actor, err)
				}
			}
			if _, err = c.DeleteConnectedInstance(ctx, "owner", instance.ID, "wrong", "doodship.test:"+instance.ID); !errors.Is(err, ErrInvalid) {
				t.Fatal("name confirmation ignored", err)
			}
			if _, err = c.AuthenticateInstance(ctx, credential); err != nil {
				t.Fatal("failed deletion revoked connection", err)
			}
			old, err := c.DeleteConnectedInstance(ctx, "owner", instance.ID, instance.Slug, "doodship.test:"+instance.ID)
			if err != nil || old != grant {
				t.Fatal("delete", old, err)
			}
			for _, query := range []string{
				`SELECT count(*) FROM tenants WHERE id=?`,
				`SELECT count(*) FROM tenant_memberships WHERE tenant_id=?`,
				`SELECT count(*) FROM tenant_handoffs WHERE tenant_id=?`,
				`SELECT count(*) FROM tenant_workload_hosts WHERE tenant_id=?`,
				`SELECT count(*) FROM tenant_usage WHERE tenant_id=?`,
				`SELECT count(*) FROM tenant_provisioning WHERE tenant_id=?`,
				`SELECT count(*) FROM tenant_workos_organizations WHERE tenant_id=?`,
				`SELECT count(*) FROM instance_connections WHERE tenant_id=?`,
			} {
				var n int
				if err = c.db.QueryRowContext(ctx, c.q(query), instance.ID).Scan(&n); err != nil || n != 0 {
					t.Fatal(query, n, err)
				}
			}
			if _, err = c.AuthenticateInstance(ctx, credential); err == nil {
				t.Fatal("deleted credential accepted")
			}
			if _, err = c.AuthenticateSession(ctx, session, TenantAudience(instance.ID)); err == nil {
				t.Fatal("deleted remote session accepted")
			}
			if _, err = c.LoginOrigin(ctx, session); !errors.Is(err, ErrNotFound) {
				t.Fatal("remote login origin retained", err)
			}
			if _, err = c.AuthenticateSession(ctx, platformSession, AudiencePlatform); err != nil {
				t.Fatal("platform account session removed", err)
			}
			if _, err = c.Tenant(ctx, other.ID); err != nil {
				t.Fatal("other instance removed", err)
			}
			pending, err := c.PendingOrganizationDeletions(ctx)
			if err != nil || len(pending) != 1 || pending[0].OrganizationID != "org_example" {
				t.Fatal("cleanup lost", pending, err)
			}
			change, err := c.DNSChange(ctx, record.ID)
			if err != nil || !change.Delete || change.Generation <= record.Generation {
				t.Fatal("DNS deletion lost", change, err)
			}
			if _, err = c.CreateOwnConnectedTenant(ctx, "outsider", instance.Slug, instance.Name); !errors.Is(err, ErrConflict) {
				t.Fatal("deleted webhook address can be taken by another account", err)
			}
			if _, err = c.CreateTenant(ctx, CreateTenantInput{CreatorID: "platform", InitialOwnerID: "outsider", Slug: instance.Slug, Name: instance.Name, BackendMode: BackendConnected}); !errors.Is(err, ErrConflict) {
				t.Fatal("platform admin reassigned deleted address", err)
			}
			if _, err = c.CreateOwnConnectedTenant(ctx, "owner", instance.Slug, instance.Name); err != nil {
				t.Fatal("name cannot be reused", err)
			}
		})
	}
}

func TestDeleteInstanceWithPendingEnrollmentAndLegacyBackend(t *testing.T) {
	c := testCatalog(t, false)
	seedUsers(t, c)
	ctx := context.Background()
	instance, err := c.CreateOwnConnectedTenant(ctx, "owner", "example", "Example")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := c.CreateInstanceEnrollment(ctx, "owner", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteConnectedInstance(ctx, "owner", instance.ID, instance.Slug, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.EnrollInstance(ctx, token, "server", "", true); err == nil {
		t.Fatal("deleted enrollment accepted")
	}
	legacy := seedTenant(t, c, "legacy")
	if _, err = c.DeleteConnectedInstance(ctx, "owner", legacy.ID, legacy.Slug, ""); !errors.Is(err, ErrDenied) {
		t.Fatal("legacy operational database deletion allowed", err)
	}
	if _, err = c.Tenant(ctx, legacy.ID); err != nil {
		t.Fatal("legacy instance changed", err)
	}
}

func TestDeleteInstanceRacingEnrollment(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[pg], func(t *testing.T) {
			c := testCatalog(t, pg)
			seedUsers(t, c)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			instance, err := c.CreateOwnConnectedTenant(ctx, "owner", "example", "Example")
			if err != nil {
				t.Fatal(err)
			}
			token, _, err := c.CreateInstanceEnrollment(ctx, "owner", instance.ID)
			if err != nil {
				t.Fatal(err)
			}
			var wait sync.WaitGroup
			wait.Add(1)
			go func() { defer wait.Done(); _, _, _ = c.EnrollInstance(ctx, token, "server", "", true) }()
			if _, err = c.DeleteConnectedInstance(ctx, "owner", instance.ID, instance.Slug, ""); err != nil {
				t.Fatal(err)
			}
			wait.Wait()
			active, err := c.ActiveInstanceGrants(ctx)
			if err != nil || len(active) != 0 {
				t.Fatal("grant survived deletion", active, err)
			}
		})
	}
}

func TestFailedInstanceDeletionRollsBackRevocation(t *testing.T) {
	c := testCatalog(t, false)
	seedUsers(t, c)
	ctx := context.Background()
	instance, err := c.CreateOwnConnectedTenant(ctx, "owner", "example", "Example")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := c.CreateInstanceEnrollment(ctx, "owner", instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, credential, err := c.EnrollInstance(ctx, token, "server", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.db.ExecContext(ctx, `CREATE TRIGGER fail_instance_deletion BEFORE DELETE ON tenants BEGIN SELECT RAISE(ABORT,'deletion failed'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = c.DeleteConnectedInstance(ctx, "owner", instance.ID, instance.Slug, "doodship.test:"+instance.ID); err == nil {
		t.Fatal("deletion failure hidden")
	}
	if _, err = c.AuthenticateInstance(ctx, credential); err != nil {
		t.Fatal("failed transaction revoked access", err)
	}
	if _, err = c.Membership(ctx, instance.ID, "owner"); err != nil {
		t.Fatal("failed transaction removed membership", err)
	}
	var reservations int
	if err = c.db.QueryRowContext(ctx, c.q(`SELECT count(*) FROM instance_name_reservations WHERE slug=?`), instance.Slug).Scan(&reservations); err != nil || reservations != 0 {
		t.Fatal("failed transaction reserved name", reservations, err)
	}
	pending, err := c.PendingOrganizationDeletions(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatal("failed transaction scheduled provider deletion", pending, err)
	}
}

package tenancy

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstanceEnrollmentRequiresOwnerAndSingleUse(t *testing.T) {
	for _, pg := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[pg], func(t *testing.T) {
			c := testCatalog(t, pg)
			seedUsers(t, c)
			ctx := context.Background()
			tenant, err := c.CreateOwnConnectedTenant(ctx, "owner", "connected", "Connected")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = c.CreateInstanceEnrollment(ctx, "platform", tenant.ID); !errors.Is(err, ErrDenied) {
				t.Fatalf("admin without membership: %v", err)
			}
			token, _, err := c.CreateInstanceEnrollment(ctx, "owner", tenant.ID)
			if err != nil {
				t.Fatal(err)
			}
			var accepted atomic.Int32
			var wait sync.WaitGroup
			for i := 0; i < 8; i++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					_, _, e := c.EnrollInstance(ctx, token, "installation", "http://192.168.2.14:8080", false)
					if e == nil {
						accepted.Add(1)
					}
				}()
			}
			wait.Wait()
			if accepted.Load() != 1 {
				t.Fatalf("enrollment accepted %d times", accepted.Load())
			}
			if _, _, err = c.CreateInstanceEnrollment(ctx, "owner", tenant.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("live installation replaced without disconnect: %v", err)
			}
		})
	}
}
func TestInstanceRevocationAndEnrollmentExpiry(t *testing.T) {
	c := testCatalog(t, false)
	seedUsers(t, c)
	ctx := context.Background()
	tenant, err := c.CreateOwnConnectedTenant(ctx, "owner", "connected", "Connected")
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := c.CreateInstanceEnrollment(ctx, "owner", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := c.now()
	c.now = func() time.Time { return now.Add(11 * time.Minute) }
	if _, _, err = c.EnrollInstance(ctx, token, "installation", "https://local.example.test", false); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now }
	token, _, err = c.CreateInstanceEnrollment(ctx, "owner", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, credential, err := c.EnrollInstance(ctx, token, "installation", "https://local.example.test", false)
	if err != nil {
		t.Fatal(err)
	}
	if connection.RemoteAccess || connection.OwnerSubject != "owner" {
		t.Fatalf("unexpected grant: %+v", connection)
	}
	session, err := c.CreateSession(ctx, "owner", TenantAudience(tenant.ID), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthenticateInstance(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RevokeInstance(ctx, "owner", tenant.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.AuthenticateInstance(ctx, credential); err == nil {
		t.Fatal("revoked credential accepted")
	}
	if _, err = c.AuthenticateSession(ctx, session, TenantAudience(tenant.ID)); err == nil {
		t.Fatal("revoked remote session accepted")
	}
	token, _, err = c.CreateInstanceEnrollment(ctx, "owner", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	reconnected, newCredential, err := c.EnrollInstance(ctx, token, "other-installation", "https://new.example.test", true)
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.Generation <= connection.Generation || newCredential == credential {
		t.Fatal("grant reused")
	}
	if err = c.UpdateInstancePolicy(ctx, connection, "https://stale.example.test", true); !errors.Is(err, ErrDenied) {
		t.Fatalf("stale generation changed policy: %v", err)
	}
}

func TestConnectedUsageRetentionKeepsLegacyHistory(t *testing.T) {
	c := testCatalog(t, false)
	seedUsers(t, c)
	ctx := context.Background()
	connected, err := c.CreateOwnConnectedTenant(ctx, "owner", "connected", "Connected")
	if err != nil {
		t.Fatal(err)
	}
	legacy := seedTenant(t, c, "legacy")
	day := c.now().UTC().Truncate(24 * time.Hour).Add(-95 * 24 * time.Hour)
	for _, id := range []string{connected.ID, legacy.ID} {
		if err = c.SaveUsage(ctx, Usage{TenantID: id, PeriodStart: day, PeriodEnd: day.Add(24 * time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.PruneCredentials(ctx); err != nil {
		t.Fatal(err)
	}
	old, err := c.TenantUsage(ctx, connected.ID, day, day.Add(24*time.Hour))
	if err != nil || len(old) != 0 {
		t.Fatal("connected history was not pruned", err)
	}
	old, err = c.TenantUsage(ctx, legacy.ID, day, day.Add(24*time.Hour))
	if err != nil || len(old) != 1 {
		t.Fatal("legacy history changed", err)
	}
}

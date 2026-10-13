package tenancy

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWorkloadHostOwnershipAcrossCatalogBackends(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := t.Context()
			if err := c.CreateUser(ctx, User{ID: "admin", Name: "Admin", Email: "admin@example.test", PlatformAdmin: true, EmailVerified: true}); err != nil {
				t.Fatal(err)
			}
			a, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "alpha", Name: "Alpha", CreatorID: "admin", InitialOwnerID: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			b, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "bravo", Name: "Bravo", CreatorID: "admin", InitialOwnerID: "admin"})
			if err != nil {
				t.Fatal(err)
			}
			owner := WorkloadHost{Hostname: "p223-abcd-alpha.doodship.com", TenantID: a.ID, AppID: "app", ProjectID: "project", ServerID: "server"}
			if err := c.ReserveWorkloadHost(ctx, owner); err != nil {
				t.Fatal(err)
			}
			if err := c.ReserveWorkloadHost(ctx, owner); err != nil {
				t.Fatal("reservation replay failed", err)
			}
			forged := owner
			forged.TenantID = b.ID
			if err := c.ReserveWorkloadHost(ctx, forged); !errors.Is(err, ErrDenied) {
				t.Fatal("tenant stole host", err)
			}
			if err := c.ReleaseWorkloadHost(ctx, b.ID, owner.AppID, owner.ServerID); err != nil {
				t.Fatal(err)
			}
			saved, err := c.WorkloadHost(ctx, owner.Hostname)
			if err != nil || saved != owner {
				t.Fatal("foreign cleanup changed ownership", saved, err)
			}
			if err := c.ReleaseWorkloadHost(ctx, a.ID, owner.AppID, owner.ServerID); err != nil {
				t.Fatal(err)
			}
			if _, err := c.WorkloadHost(ctx, owner.Hostname); !errors.Is(err, ErrNotFound) {
				t.Fatal("released host remains", err)
			}
		})
	}
}

func TestConnectedInstanceHostLimitAcrossCatalogBackends(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) {
			c := testCatalog(t, postgres)
			seedUsers(t, c)
			ctx := t.Context()
			a, err := c.CreateOwnConnectedTenant(ctx, "owner", "alpha-connected", "Alpha")
			if err != nil {
				t.Fatal(err)
			}
			b, err := c.CreateOwnConnectedTenant(ctx, "owner", "bravo-connected", "Bravo")
			if err != nil {
				t.Fatal(err)
			}
			const limit = 3
			original := WorkloadHost{Hostname: "seed-alpha.example", TenantID: a.ID, AppID: "seed", ProjectID: "project", ServerID: "server"}
			if err = c.reserveWorkloadHost(ctx, original, limit); err != nil {
				t.Fatal(err)
			}
			oversized := original
			oversized.Hostname = "oversized-alpha.example"
			oversized.AppID = strings.Repeat("a", 257)
			if err = c.reserveWorkloadHost(ctx, oversized, limit); !errors.Is(err, ErrInvalid) {
				t.Fatal("oversized connected metadata was accepted", err)
			}
			var accepted atomic.Int32
			var wait sync.WaitGroup
			failures := make(chan error, 12)
			for i := 0; i < 12; i++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					host := WorkloadHost{Hostname: fmt.Sprintf("host-%d-alpha.example", i), TenantID: a.ID, AppID: fmt.Sprintf("app-%d", i), ProjectID: "project", ServerID: "server"}
					e := c.reserveWorkloadHost(ctx, host, limit)
					if e == nil {
						accepted.Add(1)
					} else if !errors.Is(e, ErrWorkloadHostLimit) {
						failures <- e
					}
				}()
			}
			wait.Wait()
			close(failures)
			for failure := range failures {
				t.Error("unexpected concurrent reservation error", failure)
			}
			if accepted.Load() != limit-1 {
				t.Fatalf("accepted %d new hosts with only %d remaining slots", accepted.Load(), limit-1)
			}
			rows, err := c.instanceWorkloadHosts(ctx, a.ID, limit)
			if err != nil || len(rows) != limit {
				t.Fatalf("inventory length %d err %v", len(rows), err)
			}
			for _, host := range rows {
				if host.TenantID != a.ID {
					t.Fatal("foreign instance appeared in inventory")
				}
			}
			if err = c.reserveWorkloadHost(ctx, original, limit); err != nil {
				t.Fatal("idempotent replay at capacity failed", err)
			}
			forged := original
			forged.TenantID = b.ID
			if err = c.reserveWorkloadHost(ctx, forged, limit); !errors.Is(err, ErrDenied) {
				t.Fatal("foreign instance stole reservation", err)
			}
			independent := WorkloadHost{Hostname: "host-bravo.example", TenantID: b.ID, AppID: "independent", ProjectID: "project", ServerID: "server"}
			if err = c.reserveWorkloadHost(ctx, independent, limit); err != nil {
				t.Fatal("one instance consumed another's capacity", err)
			}
			if err = c.ReleaseWorkloadHost(ctx, a.ID, original.AppID, original.ServerID); err != nil {
				t.Fatal(err)
			}
			replacement := original
			replacement.Hostname = "replacement-alpha.example"
			replacement.AppID = "replacement"
			if err = c.reserveWorkloadHost(ctx, replacement, limit); err != nil {
				t.Fatal("released slot was not reusable", err)
			}
			// Simulate an old/imported catalog already beyond its configured capacity.
			if _, err = c.db.ExecContext(ctx, c.q(`INSERT INTO tenant_workload_hosts(hostname,tenant_id,app_id,project_id,server_id) VALUES(?,?,?,?,?)`), "legacy-overflow-alpha.example", a.ID, "overflow", "project", "server"); err != nil {
				t.Fatal(err)
			}
			if partial, err := c.instanceWorkloadHosts(ctx, a.ID, limit); !errors.Is(err, ErrWorkloadHostLimit) || partial != nil {
				t.Fatalf("over-capacity inventory was silently truncated: %d rows, %v", len(partial), err)
			}
		})
	}
}

func TestEmbeddedWorkloadHostReservationsKeepExistingBehavior(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) {
			c := testCatalog(t, postgres)
			seedUsers(t, c)
			ctx := t.Context()
			tenant, err := c.CreateTenant(ctx, CreateTenantInput{Slug: "embedded", Name: "Embedded", CreatorID: "platform", InitialOwnerID: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 4; i++ {
				if err = c.reserveWorkloadHost(ctx, WorkloadHost{Hostname: fmt.Sprintf("embedded-%d.example", i), TenantID: tenant.ID, AppID: fmt.Sprintf("app-%d", i), ProjectID: "project", ServerID: "server"}, 1); err != nil {
					t.Fatal("connected cap was applied to embedded instance", err)
				}
			}
			if rows, err := c.instanceWorkloadHosts(ctx, tenant.ID, 1); err != nil || len(rows) != 4 {
				t.Fatal("embedded inventory behavior changed", len(rows), err)
			}
		})
	}
}

package tenancy

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTenantOnboardingRequiresOwnerApproval(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite", true: "postgres"}[postgres], func(t *testing.T) {
			c := testCatalog(t, postgres)
			ctx := context.Background()
			seedUsers(t, c)
			tenant, err := c.CreateOwnTenant(ctx, "member", "my-team", "My team")
			if err != nil {
				t.Fatal(err)
			}
			owner, err := c.Membership(ctx, tenant.ID, "member")
			if err != nil || owner.Role != RoleOwner {
				t.Fatalf("owner: %+v %v", owner, err)
			}
			user, _ := c.User(ctx, "member")
			if user.PlatformAdmin {
				t.Fatal("self-service granted platform admin")
			}
			if _, err = c.CreateTenant(ctx, CreateTenantInput{CreatorID: "member", InitialOwnerID: "outsider", Slug: "other", Name: "Other"}); !errors.Is(err, ErrDenied) {
				t.Fatal("member assigned a different owner", err)
			}
			if err = c.SetTenantState(ctx, tenant.ID, StateActive); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err = c.RequestTenantJoin(ctx, "outsider", "MY-TEAM"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = c.CreateSession(ctx, "outsider", TenantAudience(tenant.ID), time.Hour); !errors.Is(err, ErrDenied) {
				t.Fatal("request granted access", err)
			}
			requests, err := c.TenantJoinRequests(ctx, "member", tenant.ID)
			if err != nil || len(requests) != 1 || requests[0].ID != "outsider" {
				t.Fatalf("requests: %+v %v", requests, err)
			}
			if _, err = c.TenantJoinRequests(ctx, "platform", tenant.ID); err == nil {
				t.Fatal("platform admin saw join requests without membership")
			}
			pending, err := c.PendingJoinRequests(ctx, "outsider")
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			other := seedTenant(t, c, "sibling")
			if requests, err = c.TenantJoinRequests(ctx, "owner", other.ID); err != nil || len(requests) != 0 {
				t.Fatal("request crossed tenants", err)
			}
			if err = c.SetMembership(ctx, "member", Membership{TenantID: tenant.ID, UserID: "outsider", Role: RoleMember}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.CreateSession(ctx, "outsider", TenantAudience(tenant.ID), time.Hour); err != nil {
				t.Fatal("approved user cannot enter", err)
			}
			if pending, err = c.PendingJoinRequests(ctx, "outsider"); err != nil || len(pending) != 0 {
				t.Fatal("approved request remained pending", err)
			}
			if err = c.DeleteMembership(ctx, "member", tenant.ID, "outsider"); err != nil {
				t.Fatal(err)
			}
			if err = c.RequestTenantJoin(ctx, "outsider", tenant.Slug); !errors.Is(err, ErrDenied) {
				t.Fatal("removed member requested access again", err)
			}
			if err = c.SetUserState(ctx, "outsider", StateDisabled); err != nil {
				t.Fatal(err)
			}
			if _, err = c.CreateOwnTenant(ctx, "outsider", "disabled", "Disabled"); !errors.Is(err, ErrDenied) {
				t.Fatal("disabled user created tenant", err)
			}
		})
	}
}

package tenancy

import (
	"context"
	"testing"
	"time"
)

func TestCertificateTokenIsTenantScopedAndRevocable(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			c := testCatalog(t, postgres)
			seedUsers(t, c)
			tenant := seedTenant(t, c, "alpha")
			other := seedTenant(t, c, "bravo")
			ctx := context.Background()
			if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "member", Role: RoleMember}); err != nil {
				t.Fatal(err)
			}
			for _, user := range []string{"platform", "member", "outsider"} {
				if _, _, err := c.RotateCertificateToken(ctx, tenant.ID, user); err == nil {
					t.Fatal("nonadmin issued credential", user)
				}
			}
			token, expires, err := c.RotateCertificateToken(ctx, tenant.ID, "owner")
			if err != nil {
				t.Fatal(err)
			}
			if time.Until(expires) > CertificateTokenLifetime {
				t.Fatal("unbounded credential expiry")
			}
			var saved string
			if err := c.db.QueryRowContext(ctx, c.q(`SELECT token_hash FROM tenant_certificate_tokens WHERE tenant_id=?`), tenant.ID).Scan(&saved); err != nil || saved == token {
				t.Fatal("credential not hashed", err)
			}
			if err := c.AuthenticateCertificateToken(ctx, tenant.ID, token); err != nil {
				t.Fatal(err)
			}
			if err := c.AuthenticateCertificateToken(ctx, other.ID, token); err == nil {
				t.Fatal("cross tenant credential accepted")
			}
			if _, err := c.AuthenticateSession(ctx, token, TenantAudience(tenant.ID)); err == nil {
				t.Fatal("installer became browser session")
			}
			rotated, _, err := c.RotateCertificateToken(ctx, tenant.ID, "owner")
			if err != nil {
				t.Fatal(err)
			}
			if err := c.AuthenticateCertificateToken(ctx, tenant.ID, token); err == nil {
				t.Fatal("old credential survived rotation")
			}
			if err := c.RevokeCertificateToken(ctx, tenant.ID, "member"); err == nil {
				t.Fatal("member revoked credential")
			}
			if err := c.RevokeCertificateToken(ctx, tenant.ID, "owner"); err != nil {
				t.Fatal(err)
			}
			if err := c.AuthenticateCertificateToken(ctx, tenant.ID, rotated); err == nil {
				t.Fatal("revoked credential accepted")
			}
			token, _, err = c.RotateCertificateToken(ctx, tenant.ID, "owner")
			if err != nil {
				t.Fatal(err)
			}
			c.now = func() time.Time { return time.Now().Add(CertificateTokenLifetime + time.Minute) }
			if err := c.AuthenticateCertificateToken(ctx, tenant.ID, token); err == nil {
				t.Fatal("expired credential accepted")
			}
			c.now = time.Now
			if err := c.SetMembership(ctx, "owner", Membership{TenantID: tenant.ID, UserID: "member", Role: RoleOwner}); err != nil {
				t.Fatal(err)
			}
			if err := c.SetMembership(ctx, "member", Membership{TenantID: tenant.ID, UserID: "owner", Role: RoleMember}); err != nil {
				t.Fatal(err)
			}
			if err := c.AuthenticateCertificateToken(ctx, tenant.ID, token); err == nil {
				t.Fatal("credential survived issuer demotion")
			}
		})
	}
}

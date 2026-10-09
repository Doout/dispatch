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

func TestHandoffSessionHonorsConfiguredDuration(t *testing.T) {
	for _, postgres := range []bool{false, true} {
		name := "sqlite"
		if postgres {
			name = "postgres"
		}
		t.Run(name, func(t *testing.T) {
			c := testCatalog(t, postgres)
			seedUsers(t, c)
			tenant := seedTenant(t, c, "alpha")
			ctx := context.Background()
			now := time.Now().UTC()
			c.now = func() time.Time { return now }
			verifier := strings.Repeat("v", 43)
			hash := sha256.Sum256([]byte(verifier))
			origin := "https://alpha.dispatch.example.test"
			code, err := c.CreateHandoff(ctx, HandoffRequest{UserID: "owner", TenantID: tenant.ID, Origin: origin, CodeChallenge: base64.RawURLEncoding.EncodeToString(hash[:])})
			if err != nil {
				t.Fatal(err)
			}
			exchange := HandoffExchange{Code: code, TenantID: tenant.ID, Origin: origin, CodeVerifier: verifier, SessionDuration: 13 * time.Hour}
			if _, err = c.ExchangeHandoff(ctx, exchange); !errors.Is(err, ErrInvalid) {
				t.Fatal("accepted overlong duration", err)
			}
			exchange.SessionDuration = 30 * time.Minute
			token, err := c.ExchangeHandoff(ctx, exchange)
			if err != nil {
				t.Fatal(err)
			}
			c.now = func() time.Time { return now.Add(29 * time.Minute) }
			if _, err = c.AuthenticateSession(ctx, token, TenantAudience(tenant.ID)); err != nil {
				t.Fatal("session expired early", err)
			}
			c.now = func() time.Time { return now.Add(31 * time.Minute) }
			if _, err = c.AuthenticateSession(ctx, token, TenantAudience(tenant.ID)); !errors.Is(err, ErrExpired) {
				t.Fatal("session exceeded configured duration", err)
			}
		})
	}
}

package tenancy

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDNSProviderBindingSurvivesReopen(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		ctx := t.Context()
		const target = "cloudflare:0123456789abcdef0123456789abcdef"
		if err := c.BindDNSProvider(ctx, target); err != nil {
			t.Fatal(err)
		}
		c = reopenDNSCatalog(t, c)
		if err := c.BindDNSProvider(ctx, target); err != nil {
			t.Fatal("same destination rejected after restart", err)
		}
		if err := c.BindDNSProvider(ctx, "cloudflare:another-zone"); !errors.Is(err, ErrConflict) {
			t.Fatal("destination changed without migration", err)
		}
		if err := c.BindDNSProvider(ctx, target); err != nil {
			t.Fatal("rejected change overwrote the binding", err)
		}
		for _, table := range []string{"tenant_zone_records", "tenant_dns_changes"} {
			var count int
			if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil || count != 0 {
				t.Fatal("provider identity entered published DNS data", table, count, err)
			}
		}
	})
}

func TestDNSProviderBindingRejectsInvalidIdentity(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, c *Catalog) {
		for _, target := range []string{"", " ", " cloudflare:zone", "cloudflare:zone\n", "cloudflare:\x00zone", strings.Repeat("x", 257)} {
			if err := c.BindDNSProvider(t.Context(), target); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid destination %q: %v", target, err)
			}
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := c.BindDNSProvider(ctx, "cancelled"); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled binding was accepted", err)
		}
		if err := c.BindDNSProvider(t.Context(), strings.Repeat("x", 256)); err != nil {
			t.Fatal("invalid or cancelled request persisted a binding", err)
		}
	})
}

func TestDNSProviderBindingConcurrentConnections(t *testing.T) {
	eachDNSCatalog(t, func(t *testing.T, first *Catalog) {
		second, err := Open(t.Context(), dnsCatalogURL(t, first))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { second.Close() })
		type result struct {
			target string
			err    error
		}
		start := make(chan struct{})
		results := make(chan result, 2)
		for i, c := range []*Catalog{first, second} {
			target := []string{"cloudflare:first-zone", "cloudflare:second-zone"}[i]
			go func() {
				<-start
				results <- result{target: target, err: c.BindDNSProvider(t.Context(), target)}
			}()
		}
		close(start)
		winner, loser := <-results, <-results
		if winner.err != nil {
			winner, loser = loser, winner
		}
		if winner.err != nil || !errors.Is(loser.err, ErrConflict) {
			t.Fatal("competing bindings must produce one winner and one conflict", winner, loser)
		}
		for _, c := range []*Catalog{first, second} {
			if err := c.BindDNSProvider(t.Context(), winner.target); err != nil {
				t.Fatal("winning destination unavailable to another connection", err)
			}
			if err := c.BindDNSProvider(t.Context(), loser.target); !errors.Is(err, ErrConflict) {
				t.Fatal("losing destination overwrote binding", err)
			}
		}
	})
}

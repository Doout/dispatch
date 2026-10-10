package hosted

import (
	"context"
	"errors"
	"time"

	"github.com/doout/dispatch/internal/dnsprovider"
	"github.com/doout/dispatch/internal/tenancy"
)

// Callers hold dnsMu from saving intent through publication. Catalog versions
// also prevent an acknowledgement from clearing a newer change after restart.
func (s *Server) publishDNSChange(ctx context.Context, change tenancy.DNSChange) error {
	r := change.Record
	record := dnsprovider.Record{ID: r.ID, Name: r.Name, Type: r.Type, Values: r.Values, TTL: r.TTL}
	var err error
	if change.Delete {
		err = s.Config.DNSProvider.Delete(ctx, record)
	} else {
		err = s.Config.DNSProvider.Ensure(ctx, record)
	}
	if err != nil {
		return err
	}
	return s.Catalog.CompleteDNSChange(ctx, r.ID, change.Generation)
}

func (s *Server) syncDNSRecord(ctx context.Context, id string) error {
	change, err := s.Catalog.DNSChange(ctx, id)
	if errors.Is(err, tenancy.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.publishDNSChange(ctx, change)
}

func (s *Server) syncTenantDNS(ctx context.Context, tenant string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		changes, err := s.Catalog.PendingTenantDNSChanges(ctx, tenant, 1000)
		if err != nil {
			return err
		}
		var joined error
		failedNames := make(map[string]bool)
		for _, change := range changes {
			if failedNames[change.Record.Name] {
				continue
			}
			if err := s.publishDNSChange(ctx, change); err != nil {
				// In particular, don't publish an A record after its conflicting
				// old CNAME failed to delete. Other names can still make progress.
				failedNames[change.Record.Name] = true
				joined = errors.Join(joined, err)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		if joined != nil || len(changes) < 1000 {
			return joined
		}
	}
}

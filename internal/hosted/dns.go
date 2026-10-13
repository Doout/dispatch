package hosted

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"sort"

	"github.com/doout/dispatch/internal/tenancy"
)

const platformOwner = "platform"

func recordID(owner, name, kind string) string {
	sum := sha256.Sum256([]byte(owner + ":" + name + ":" + kind))
	return hex.EncodeToString(sum[:])
}

// putRecord saves controller-owned intent with the expected catalog generation.
func (s *Server) putRecord(ctx context.Context, desired tenancy.ZoneRecord) error {
	_, records, err := s.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	sort.Strings(desired.Values)
	for _, existing := range records {
		if existing.ID != desired.ID {
			continue
		}
		if existing.OwnerID != desired.OwnerID || existing.TenantID != desired.TenantID {
			return tenancy.ErrDenied
		}
		desired.Generation = existing.Generation
		if reflect.DeepEqual(existing, desired) {
			return nil
		}
	}
	_, err = s.Catalog.SaveZoneRecord(ctx, desired)
	return err
}

func (s *Server) prepareZone(ctx context.Context) error {
	// This private catalog marker binds the installation to its original root.
	// The DNS provider never receives it. The root console record is configured
	// once by the operator, along with the installation wildcard.
	digest := sha256.Sum256([]byte(s.Config.RootDomain))
	if err := s.putRecord(ctx, tenancy.ZoneRecord{ID: "platform-zone-config", OwnerID: platformOwner, TenantID: platformOwner, Name: "_dispatch." + s.Config.RootDomain, Type: "TXT", Values: []string{hex.EncodeToString(digest[:])}, TTL: 300}); err != nil {
		return err
	}
	if err := s.Catalog.BindDNSProvider(ctx, s.Config.DNSProvider.Target()); err != nil {
		if errors.Is(err, tenancy.ErrConflict) {
			return errors.New("DNS provider target differs from the catalog; changing provider or zone requires a migration")
		}
		return err
	}
	_, records, err := s.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.OwnerID == platformOwner && record.ID != "platform-zone-config" {
			if err := s.Catalog.DeleteZoneRecord(ctx, record.ID, platformOwner, record.Generation); err != nil {
				return err
			}
		}
	}
	return nil
}

// Tenant consoles use the installation wildcard. Retire only records that the
// old controller created; operator records and existing custom records survive.
func (s *Server) prepareTenantDNS(ctx context.Context, tenant tenancy.Tenant) error {
	host := tenant.Slug + "." + s.Config.RootDomain
	_, records, err := s.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.TenantID != tenant.ID {
			continue
		}
		legacy := record.OwnerID == "console:"+tenant.ID && record.Name == host || record.OwnerID == "workloads:"+tenant.ID && record.Name == "*."+host || record.OwnerID == "acme:"+tenant.ID && record.Name == "_acme-challenge."+host
		if legacy {
			if err := s.Catalog.DeleteZoneRecord(ctx, record.ID, record.OwnerID, record.Generation); err != nil {
				return err
			}
		}
	}
	if err := s.Catalog.SaveDomain(ctx, tenancy.Domain{ID: "console:" + tenant.ID, TenantID: tenant.ID, Hostname: host, Kind: "console", State: tenancy.StateActive}); err != nil {
		return err
	}
	// Leave legacy metadata disabled so it cannot authorize deeper hostnames.
	domains, err := s.Catalog.Domains(ctx, tenant.ID)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		if domain.Kind == "workloads" {
			domain.State = tenancy.StateDisabled
			if err := s.Catalog.SaveDomain(ctx, domain); err != nil {
				return err
			}
		}
	}
	return nil
}

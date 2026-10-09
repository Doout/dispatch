package hosted

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"

	"github.com/doout/dispatch/internal/authoritativedns"
	"github.com/doout/dispatch/internal/tenancy"
)

const platformOwner = "platform"

func recordID(owner, name, kind string) string {
	sum := sha256.Sum256([]byte(owner + ":" + name + ":" + kind))
	return hex.EncodeToString(sum[:])
}

func (s *Server) dnsSnapshot(ctx context.Context) (authoritativedns.Snapshot, error) {
	generation, records, err := s.Catalog.ZoneRecords(ctx)
	if err != nil {
		return authoritativedns.Snapshot{}, err
	}
	snapshot := authoritativedns.Snapshot{Zone: s.Config.RootDomain, Generation: generation, Nameservers: s.Config.Nameservers, TTL: 300}
	for _, record := range records {
		snapshot.Records = append(snapshot.Records, authoritativedns.Record{ID: record.ID, OwnerID: record.OwnerID, Name: record.Name, Type: record.Type, Values: record.Values, TTL: record.TTL, Generation: record.Generation})
	}
	return snapshot, nil
}

func (s *Server) dnsSnapshotHandler() http.Handler {
	return authoritativedns.SnapshotHandler(s.Config.DNSReadToken, s.dnsSnapshot)
}

// putRecord is used only by controller reconciliation. User writes supply an
// expected generation instead, so concurrent edits cannot silently overwrite.
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

func (s *Server) addressRecords(ctx context.Context, owner, tenant, host string, addresses []string) error {
	byType := map[string][]string{}
	for _, address := range addresses {
		ip := net.ParseIP(address)
		kind := "CNAME"
		if ip != nil {
			kind = "AAAA"
			if ip.To4() != nil {
				kind = "A"
			}
		}
		byType[kind] = append(byType[kind], address)
	}
	if len(byType["CNAME"]) > 0 && len(byType) > 1 {
		return tenancy.ErrInvalid
	}
	for kind, values := range byType {
		if err := validateRecord(kind, values, 300); err != nil {
			return err
		}
	}
	// Remove conflicting old types first, so changing a CNAME to an IP address
	// or back can converge. Every intermediate snapshot remains a valid zone.
	_, records, err := s.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.OwnerID == owner && record.Name == host && (record.Type == "A" || record.Type == "AAAA" || record.Type == "CNAME") && len(byType[record.Type]) == 0 {
			if err = s.Catalog.DeleteZoneRecord(ctx, record.ID, owner, record.Generation); err != nil {
				return err
			}
		}
	}
	for kind, values := range byType {
		if err := s.putRecord(ctx, tenancy.ZoneRecord{ID: recordID(owner, host, kind), OwnerID: owner, TenantID: tenant, Name: host, Type: kind, Values: values, TTL: 300}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) prepareZone(ctx context.Context) error {
	encoded, err := json.Marshal(struct {
		Zone        string
		Nameservers []string
	}{s.Config.RootDomain, s.Config.Nameservers})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if err = s.putRecord(ctx, tenancy.ZoneRecord{ID: "platform-zone-config", OwnerID: platformOwner, TenantID: platformOwner, Name: "_dispatch." + s.Config.RootDomain, Type: "TXT", Values: []string{hex.EncodeToString(digest[:])}, TTL: 300}); err != nil {
		return err
	}

	if err := s.addressRecords(ctx, platformOwner, platformOwner, s.Config.RootDomain, s.Config.ConsoleAddresses); err != nil {
		return err
	}
	for _, ns := range s.Config.Nameservers {
		if ns == s.Config.RootDomain || strings.HasSuffix(ns, "."+s.Config.RootDomain) {
			if err := s.addressRecords(ctx, platformOwner, platformOwner, ns, s.Config.NameserverAddresses[ns]); err != nil {
				return err
			}
		}
	}
	_, records, err := s.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.OwnerID != platformOwner || record.Name == s.Config.RootDomain || record.Type != "A" && record.Type != "AAAA" {
			continue
		}
		retained := false
		for _, ns := range s.Config.Nameservers {
			if record.Name == ns {
				retained = true
				break
			}
		}
		if !retained {
			if err := s.Catalog.DeleteZoneRecord(ctx, record.ID, platformOwner, record.Generation); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) prepareTenantDNS(ctx context.Context, tenant tenancy.Tenant) error {
	host := tenant.Slug + "." + s.Config.RootDomain
	if err := s.Catalog.SaveDomain(ctx, tenancy.Domain{ID: "console:" + tenant.ID, TenantID: tenant.ID, Hostname: host, Kind: "console", State: tenancy.StateActive}); err != nil {
		return err
	}
	if err := s.addressRecords(ctx, "console:"+tenant.ID, tenant.ID, host, s.Config.ConsoleAddresses); err != nil {
		return err
	}
	if s.Config.WorkloadGateway == "" {
		if err := s.addressRecords(ctx, "workloads:"+tenant.ID, tenant.ID, "*."+host, nil); err != nil {
			return err
		}
		return s.Catalog.SaveDomain(ctx, tenancy.Domain{ID: "workloads:" + tenant.ID, TenantID: tenant.ID, Hostname: "*." + host, Kind: "workloads", State: tenancy.StateDisabled})
	}
	if err := s.Catalog.SaveDomain(ctx, tenancy.Domain{ID: "workloads:" + tenant.ID, TenantID: tenant.ID, Hostname: "*." + host, Kind: "workloads", State: tenancy.StateActive}); err != nil {
		return err
	}
	return s.addressRecords(ctx, "workloads:"+tenant.ID, tenant.ID, "*."+host, []string{s.Config.WorkloadGateway})
}

func (s *Server) tenantOwner(r *http.Request, tenant tenancy.Tenant) bool {
	u, err := s.Catalog.AuthenticateSession(r.Context(), requestToken(r, tenantCookie), tenancy.TenantAudience(tenant.ID))
	if err != nil {
		return false
	}
	m, err := s.Catalog.Membership(r.Context(), tenant.ID, u.ID)
	return err == nil && (m.Role == tenancy.RoleOwner || m.Role == tenancy.RoleAdmin)
}

func (s *Server) tenantDNS(w http.ResponseWriter, r *http.Request, tenant tenancy.Tenant) {
	if !s.tenantOwner(r, tenant) {
		problem(w, http.StatusForbidden, "Tenant administrator access required.")
		return
	}
	_, records, err := s.Catalog.ZoneRecords(r.Context())
	if err != nil {
		catalogError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		items := []tenancy.ZoneRecord{}
		for _, record := range records {
			if record.TenantID == tenant.ID && !strings.HasPrefix(record.OwnerID, "acme:") {
				items = append(items, record)
			}
		}
		respond(w, http.StatusOK, items)
		return
	}
	var input struct {
		Name       string   `json:"name"`
		Type       string   `json:"type"`
		Values     []string `json:"values"`
		TTL        uint32   `json:"ttl"`
		Generation uint64   `json:"generation"`
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, PUT, DELETE")
		problem(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	if !decode(w, r, &input) {
		return
	}
	input.Name = strings.ToLower(strings.TrimSuffix(input.Name, "."))
	input.Type = strings.ToUpper(input.Type)
	prefix, ok := strings.CutSuffix(input.Name, "."+tenant.Slug+"."+s.Config.RootDomain)
	// Console, wildcard, and ACME records belong to the controller. Limit user
	// records to one label; this also keeps certificate coverage unambiguous.
	if !ok || !validHostname(prefix) || strings.Contains(prefix, ".") || strings.HasPrefix(prefix, "_") {
		problem(w, http.StatusBadRequest, "Choose one subdomain below this tenant.")
		return
	}
	owner := "records:" + tenant.ID
	id := recordID(owner, input.Name, input.Type)
	if r.Method == http.MethodDelete {
		if err = s.Catalog.DeleteZoneRecord(r.Context(), id, owner, input.Generation); err != nil {
			catalogError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err = validateRecord(input.Type, input.Values, input.TTL); err != nil {
		problem(w, http.StatusBadRequest, err.Error())
		return
	}
	for _, record := range records {
		if record.Name == input.Name && record.ID != id && (record.Type == "CNAME" || input.Type == "CNAME") {
			problem(w, http.StatusConflict, "A CNAME cannot share a name with another record.")
			return
		}
	}
	record, err := s.Catalog.SaveZoneRecord(r.Context(), tenancy.ZoneRecord{ID: id, OwnerID: owner, TenantID: tenant.ID, Name: input.Name, Type: input.Type, Values: input.Values, TTL: input.TTL, Generation: input.Generation})
	if err != nil {
		catalogError(w, err)
		return
	}
	respond(w, http.StatusOK, record)
}

func validateRecord(kind string, values []string, ttl uint32) error {
	if len(values) == 0 || len(values) > 100 || ttl < 60 || ttl > 86400 {
		return errors.New("Use 1 to 100 values and a TTL between 60 and 86400 seconds.")
	}
	for _, value := range values {
		switch kind {
		case "A":
			if ip := net.ParseIP(value); ip == nil || ip.To4() == nil {
				return errors.New("A records require IPv4 addresses.")
			}
		case "AAAA":
			if ip := net.ParseIP(value); ip == nil || ip.To4() != nil {
				return errors.New("AAAA records require IPv6 addresses.")
			}
		case "CNAME":
			if len(values) != 1 || !validHostname(strings.TrimSuffix(strings.ToLower(value), ".")) || net.ParseIP(value) != nil {
				return errors.New("A CNAME requires one DNS name.")
			}
		case "TXT":
			if len(value) > 255 || strings.ContainsAny(value, "\x00\r\n") {
				return errors.New("TXT values must fit in 255 bytes without line breaks.")
			}
		default:
			return errors.New("Supported record types are A, AAAA, CNAME, and TXT.")
		}
	}
	return nil
}

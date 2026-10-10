package hosted

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/doout/dispatch/internal/authoritativedns"
	"github.com/doout/dispatch/internal/tenancy"
)

type CertificateConfig struct {
	DirectoryURL  string
	Email         string
	TermsAccepted bool
}

type certificateCache struct {
	sync.RWMutex
	items map[string]authoritativedns.CertificateResult
}

type catalogSolver struct{ server *Server }

func (c catalogSolver) authorize(ctx context.Context, ch authoritativedns.Challenge) (string, error) {
	if ch.ID == "" || ch.Generation == 0 || ch.Value == "" || len(ch.Value) > 255 || strings.ContainsAny(ch.Value, "\x00\r\n") {
		return "", tenancy.ErrInvalid
	}
	name := strings.TrimSuffix(strings.ToLower(ch.Name), ".")
	if ch.OwnerID == platformOwner {
		if name != "_acme-challenge."+c.server.Config.RootDomain {
			return "", tenancy.ErrDenied
		}
		return platformOwner, nil
	}
	tenant, err := c.server.Catalog.Tenant(ctx, ch.OwnerID)
	if err != nil {
		return "", err
	}
	if name != "_acme-challenge."+tenant.Slug+"."+c.server.Config.RootDomain {
		return "", tenancy.ErrDenied
	}
	return tenant.ID, nil
}

func (c catalogSolver) Present(ctx context.Context, ch authoritativedns.Challenge) error {
	tenant, err := c.authorize(ctx, ch)
	if err != nil {
		return err
	}
	c.server.dnsMu.Lock()
	defer c.server.dnsMu.Unlock()
	// Include the issuance generation in the identity. A delayed cleanup from
	// an earlier issuance cannot remove a newer TXT value at the same name.
	desired := tenancy.ZoneRecord{ID: challengeID(ch), OwnerID: "acme:" + ch.OwnerID, TenantID: tenant, Name: strings.TrimSuffix(strings.ToLower(ch.Name), "."), Type: "TXT", Values: []string{ch.Value}, TTL: 60}
	_, records, err := c.server.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	for _, existing := range records {
		if existing.ID != desired.ID {
			continue
		}
		desired.Generation = existing.Generation
		if !reflect.DeepEqual(existing, desired) {
			return tenancy.ErrDenied
		}
		return nil
	}
	_, err = c.server.Catalog.SaveZoneRecord(ctx, desired)
	return err
}

func challengeID(ch authoritativedns.Challenge) string {
	return recordID(ch.OwnerID, ch.ID, strconv.FormatUint(ch.Generation, 10))
}

func (c catalogSolver) Wait(ctx context.Context, ch authoritativedns.Challenge) error {
	if _, err := c.authorize(ctx, ch); err != nil {
		return err
	}
	addresses := []string{}
	for _, ns := range c.server.Config.Nameservers {
		if ips := c.server.Config.NameserverAddresses[ns]; len(ips) > 0 {
			for _, ip := range ips {
				addresses = append(addresses, net.JoinHostPort(ip, "53"))
			}
		} else {
			addresses = append(addresses, net.JoinHostPort(ns, "53"))
		}
	}
	return authoritativedns.WaitForTXT(ctx, addresses, ch)
}

func (c catalogSolver) Cleanup(ctx context.Context, ch authoritativedns.Challenge) error {
	if _, err := c.authorize(ctx, ch); err != nil {
		return err
	}
	c.server.dnsMu.Lock()
	defer c.server.dnsMu.Unlock()
	_, records, err := c.server.Catalog.ZoneRecords(ctx)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.ID != challengeID(ch) {
			continue
		}
		if record.OwnerID != "acme:"+ch.OwnerID || record.Name != strings.TrimSuffix(strings.ToLower(ch.Name), ".") || !reflect.DeepEqual(record.Values, []string{ch.Value}) {
			return tenancy.ErrDenied
		}
		return c.server.Catalog.DeleteZoneRecord(ctx, record.ID, record.OwnerID, record.Generation)
	}
	return nil
}

func (s *Server) reconcileCertificate(ctx context.Context, tenant *tenancy.Tenant) error {
	if s.Config.Certificates.DirectoryURL == "" {
		return nil
	}
	owner, host := platformOwner, s.Config.RootDomain
	domains := []string{host, "*." + host}
	if tenant != nil {
		owner, host = tenant.ID, tenant.Slug+"."+s.Config.RootDomain
		domains = []string{"*." + host}
	}
	r := authoritativedns.Reconciler{DirectoryURL: s.Config.Certificates.DirectoryURL, Email: s.Config.Certificates.Email, TermsAccepted: s.Config.Certificates.TermsAccepted, StoreDirectory: filepath.Join(s.Config.DataDirectory, "certificates"), Solver: catalogSolver{s}}
	request := authoritativedns.CertificateRequest{ID: host, OwnerID: owner, Generation: 1, Domains: domains}
	if cached, err := r.Cached(request); err == nil {
		s.cacheCertificate(owner, cached)
	}
	result, err := r.Ensure(ctx, request)
	if err != nil {
		return err
	}
	s.cacheCertificate(owner, result)
	return nil
}

func (s *Server) cacheCertificate(owner string, result authoritativedns.CertificateResult) {
	s.certificates.Lock()
	defer s.certificates.Unlock()
	if s.certificates.items == nil {
		s.certificates.items = map[string]authoritativedns.CertificateResult{}
	}
	s.certificates.items[owner] = result
}

// GetCertificate serves control-plane hosts only. Workload ingress receives
// the individual tenant bundle through its tenant-authenticated endpoint.
func (s *Server) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host, ok := canonicalHost(hello.ServerName)
	if !ok {
		return nil, errors.New("unrecognized TLS server name")
	}
	if host != s.Config.RootDomain {
		slug, found := strings.CutSuffix(host, "."+s.Config.RootDomain)
		if !found || strings.Contains(slug, ".") {
			return nil, errors.New("unrecognized TLS server name")
		}
		if _, err := s.Catalog.TenantBySlug(hello.Context(), slug); err != nil {
			return nil, errors.New("unrecognized TLS server name")
		}
	}
	s.certificates.RLock()
	result, ok := s.certificates.items[platformOwner]
	s.certificates.RUnlock()
	if !ok || !result.NotAfter.After(time.Now()) {
		return nil, errors.New("console certificate unavailable")
	}
	cert, err := tls.X509KeyPair(result.CertificatePEM, result.PrivateKeyPEM)
	return &cert, err
}

func (s *Server) tenantCertificate(w http.ResponseWriter, r *http.Request, tenant tenancy.Tenant) {
	if !s.tenantOwner(r, tenant) && s.Catalog.AuthenticateCertificateToken(r.Context(), tenant.ID, certificateBearer(r)) != nil {
		problem(w, http.StatusForbidden, "Tenant administrator access required.")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		problem(w, http.StatusMethodNotAllowed, "Method not allowed.")
		return
	}
	s.certificates.RLock()
	result, ok := s.certificates.items[tenant.ID]
	s.certificates.RUnlock()
	if !ok || !result.NotAfter.After(time.Now()) {
		problem(w, http.StatusServiceUnavailable, "Certificate is not ready.")
		return
	}
	respond(w, http.StatusOK, map[string]any{"certificatePem": string(result.CertificatePEM), "privateKeyPem": string(result.PrivateKeyPEM), "notAfter": result.NotAfter, "renewAfter": result.RenewAfter})
}

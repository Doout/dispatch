// Package hosted serves the platform catalog and routes instance requests through
// customer-initiated connections. Embedded runtimes remain an explicit legacy mode.
package hosted

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/dnsprovider"
	"github.com/doout/dispatch/internal/tenancy"
)

type Config struct {
	BackendMode         string
	AllowRegistration   bool
	WorkOS              WorkOSConfig
	RootDomain          string
	ReservedTenantNames []string
	TrustedProxyCIDRs   []string
	DataDirectory       string
	ConsoleAddresses    []string
	WorkloadGateway     string
	DNSProvider         dnsprovider.Provider
	SessionDuration     time.Duration
	SMTP                SMTPConfig
	Certificates        CertificateConfig
}

func (c *Config) Validate() error {
	if c.BackendMode == "" {
		c.BackendMode = tenancy.BackendEmbedded
	}
	if c.BackendMode != tenancy.BackendEmbedded && c.BackendMode != tenancy.BackendConnected {
		return errors.New("backend mode must be embedded or connected")
	}
	reserved := tenancy.ReservedTenantNames()
	for _, name := range c.ReservedTenantNames {
		name = strings.ToLower(strings.TrimSpace(name))
		if !tenancy.ValidSlug(name) && !slices.Contains(reserved, name) && !(tenancy.ReservedTenantPrefix(name) && validHostname(name) && !strings.Contains(name, ".")) {
			return errors.New("reserved tenant names must be valid subdomain labels")
		}
		if !slices.Contains(reserved, name) {
			reserved = append(reserved, name)
		}
	}
	slices.Sort(reserved)
	c.ReservedTenantNames = reserved
	c.RootDomain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.RootDomain), "."))
	if !validHostname(c.RootDomain) || net.ParseIP(c.RootDomain) != nil || strings.Count(c.RootDomain, ".") < 1 {
		return errors.New("a DNS root domain is required")
	}
	if !filepath.IsAbs(c.DataDirectory) {
		return errors.New("hosted data directory must be absolute")
	}
	for _, address := range c.ConsoleAddresses {
		if net.ParseIP(address) == nil {
			return errors.New("console addresses must be IP addresses")
		}
	}
	if len(c.ConsoleAddresses) == 0 {
		return errors.New("at least one console address is required")
	}
	if c.DNSProvider == nil {
		return errors.New("a DNS provider is required")
	}
	if c.WorkloadGateway != "" {
		gateway := c.WorkloadGateway
		if strings.Contains(gateway, "://") {
			u, err := url.Parse(gateway)
			if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("workload gateway must be an HTTP or HTTPS ingress origin")
			}
			gateway = u.Hostname()
		}
		if net.ParseIP(gateway) == nil && !validHostname(gateway) || gateway == c.RootDomain {
			return errors.New("workload gateway must be a separate ingress address")
		}
	}
	if c.SessionDuration == 0 {
		c.SessionDuration = 12 * time.Hour
	}
	if c.SessionDuration < time.Minute || c.SessionDuration > 12*time.Hour {
		return errors.New("session duration must be between one minute and twelve hours")
	}
	if c.Certificates.DirectoryURL != "" {
		u, err := url.Parse(c.Certificates.DirectoryURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || !c.Certificates.TermsAccepted || c.Certificates.Email == "" {
			return errors.New("ACME requires an HTTPS directory, contact email, and accepted terms")
		}
	}
	for _, cidr := range c.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return errors.New("trusted proxies must use CIDR notation")
		}
	}
	if err := c.WorkOS.validate(); err != nil {
		return err
	}
	return c.SMTP.Validate()
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	return true
}

func (c Config) Origin() string                  { return "https://" + c.RootDomain }
func (c Config) TenantOrigin(slug string) string { return "https://" + slug + "." + c.RootDomain }

func (c Config) TenantNameReserved(name string) bool {
	return slices.Contains(c.ReservedTenantNames, name) || tenancy.ReservedTenantPrefix(name)
}

func canonicalHost(raw string) (string, bool) {
	if strings.ContainsAny(raw, " /\\\t\r\n@%") {
		return "", false
	}
	host := raw
	if strings.Contains(raw, ":") {
		var port string
		var err error
		host, port, err = net.SplitHostPort(raw)
		if err != nil || port != "443" {
			return "", false
		}
	}
	host = strings.ToLower(host)
	return host, validHostname(host)
}

func validOrigin(raw, expected string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == "" && raw == expected
}

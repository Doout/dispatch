// Package hosted serves the platform catalog and tenant controllers on distinct
// registered hosts. Each tenant controller receives its own operational store.
package hosted

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	RootDomain          string
	TrustedProxyCIDRs   []string
	DataDirectory       string
	ConsoleAddresses    []string
	WorkloadGateway     string
	Nameservers         []string
	NameserverAddresses map[string][]string
	DNSReadToken        string
	SessionDuration     time.Duration
	SMTP                SMTPConfig
	Certificates        CertificateConfig
}

func (c *Config) Validate() error {
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
	if len(c.Nameservers) < 2 {
		return errors.New("configure at least two authoritative nameserver names")
	}
	seen := map[string]bool{}
	for i, ns := range c.Nameservers {
		ns = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(ns), "."))
		if !validHostname(ns) || seen[ns] || ns == c.RootDomain {
			return errors.New("nameserver names must be distinct DNS names")
		}
		c.Nameservers[i], seen[ns] = ns, true
		if ns == c.RootDomain || strings.HasSuffix(ns, "."+c.RootDomain) {
			if len(c.NameserverAddresses[ns]) == 0 {
				return errors.New("nameservers inside the managed zone require glue addresses")
			}
		}
		for _, address := range c.NameserverAddresses[ns] {
			if net.ParseIP(address) == nil {
				return errors.New("nameserver glue must contain IP addresses")
			}
		}
	}
	if len(c.DNSReadToken) < 32 {
		return errors.New("DNS snapshot token must contain at least 32 characters")
	}
	if c.WorkloadGateway != "" && net.ParseIP(c.WorkloadGateway) == nil && !validHostname(c.WorkloadGateway) {
		return errors.New("workload gateway must be an IP address or DNS name")
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

package main

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/doout/dispatch/internal/hosted"
)

type configuration struct {
	Hosted           hosted.Config
	CatalogURL       string
	PostgresAdminURL string
	Address          string
	TLSMode          string
	CertificateFile  string
	KeyFile          string
}

func privateFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential files must be private regular files")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("credential file is empty")
	}
	return value, nil
}

func secret(name string) (string, error) {
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return "", nil
	}
	return privateFile(path)
}

func catalogConfiguration() (string, string, error) {
	root := os.Getenv("DISPATCH_HOSTED_DATA_DIR")
	if !filepath.IsAbs(root) {
		return "", "", errors.New("DISPATCH_HOSTED_DATA_DIR must be a new absolute hosted data directory")
	}
	database, err := secret("DISPATCH_HOSTED_CATALOG_URL")
	if err != nil {
		return "", "", err
	}
	if database != "" && !strings.HasPrefix(database, "postgres://") && !strings.HasPrefix(database, "postgresql://") {
		return "", "", errors.New("an explicit hosted catalog URL must use PostgreSQL; SQLite development uses the derived hosted catalog path")
	}
	if database == "" {
		if os.Getenv("DISPATCH_HOSTED_ALLOW_SQLITE") != "true" {
			return "", "", errors.New("set DISPATCH_HOSTED_CATALOG_URL_FILE; SQLite requires explicit DISPATCH_HOSTED_ALLOW_SQLITE=true")
		}
		database = filepath.Join(root, "catalog.sqlite")
	}
	return root, database, nil
}

func readConfiguration() (configuration, error) {
	var c configuration
	root, database, err := catalogConfiguration()
	if err != nil {
		return c, err
	}
	c.CatalogURL = database
	c.Hosted.DataDirectory = root
	c.Hosted.RootDomain = os.Getenv("DISPATCH_HOSTED_ROOT_DOMAIN")
	c.Hosted.TrustedProxyCIDRs = list(os.Getenv("DISPATCH_HOSTED_TRUSTED_PROXIES"))
	c.Hosted.ConsoleAddresses = list(os.Getenv("DISPATCH_HOSTED_CONSOLE_ADDRESSES"))
	c.Hosted.WorkloadGateway = strings.TrimSpace(os.Getenv("DISPATCH_HOSTED_WORKLOAD_GATEWAY"))
	c.Hosted.Nameservers = list(os.Getenv("DISPATCH_HOSTED_NAMESERVERS"))
	c.Hosted.NameserverAddresses = map[string][]string{}
	for _, entry := range list(os.Getenv("DISPATCH_HOSTED_NAMESERVER_ADDRESSES")) {
		name, address, ok := strings.Cut(entry, "=")
		if !ok || name == "" || address == "" {
			return c, errors.New("nameserver addresses must use name=IP entries")
		}
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		c.Hosted.NameserverAddresses[name] = append(c.Hosted.NameserverAddresses[name], address)
	}
	if c.Hosted.DNSReadToken, err = secret("DISPATCH_HOSTED_DNS_TOKEN"); err != nil {
		return c, err
	}
	if c.PostgresAdminURL, err = secret("DISPATCH_HOSTED_POSTGRES_ADMIN_URL"); err != nil {
		return c, err
	}
	if strings.HasPrefix(database, "postgres") && c.PostgresAdminURL == "" {
		return c, errors.New("PostgreSQL hosted mode requires DISPATCH_HOSTED_POSTGRES_ADMIN_URL_FILE for isolated tenant databases")
	}
	c.Hosted.SMTP = hosted.SMTPConfig{Address: os.Getenv("DISPATCH_HOSTED_SMTP_ADDRESS"), Username: os.Getenv("DISPATCH_HOSTED_SMTP_USERNAME"), From: os.Getenv("DISPATCH_HOSTED_SMTP_FROM")}
	if c.Hosted.SMTP.Password, err = secret("DISPATCH_HOSTED_SMTP_PASSWORD"); err != nil {
		return c, err
	}
	c.Hosted.Certificates = hosted.CertificateConfig{DirectoryURL: os.Getenv("DISPATCH_HOSTED_ACME_DIRECTORY"), Email: os.Getenv("DISPATCH_HOSTED_ACME_EMAIL"), TermsAccepted: os.Getenv("DISPATCH_HOSTED_ACME_ACCEPT_TERMS") == "true"}
	c.Address = os.Getenv("DISPATCH_HOSTED_ADDR")
	if c.Address == "" {
		c.Address = "127.0.0.1:8081"
	}
	if _, _, err = net.SplitHostPort(c.Address); err != nil {
		return c, errors.New("DISPATCH_HOSTED_ADDR must be host:port")
	}
	c.TLSMode = os.Getenv("DISPATCH_HOSTED_TLS_MODE")
	if c.TLSMode == "" {
		c.TLSMode = "certificate"
	}
	c.CertificateFile, c.KeyFile = os.Getenv("DISPATCH_HOSTED_TLS_CERT_FILE"), os.Getenv("DISPATCH_HOSTED_TLS_KEY_FILE")
	switch c.TLSMode {
	case "proxy":
		host, _, _ := net.SplitHostPort(c.Address)
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return c, errors.New("proxy mode must listen on a loopback IP; terminate HTTPS on the same host")
		}
	case "certificate":
		if c.CertificateFile == "" || c.KeyFile == "" {
			return c, errors.New("certificate TLS mode requires certificate and private key files")
		}
		if _, err = privateFile(c.KeyFile); err != nil {
			return c, err
		}
	case "acme":
		if c.Hosted.Certificates.DirectoryURL == "" {
			return c, errors.New("ACME TLS mode requires configured ACME issuance")
		}
		directory, _ := url.Parse(c.Hosted.Certificates.DirectoryURL)
		if directory != nil && strings.EqualFold(strings.TrimSuffix(directory.Hostname(), "."), "acme-staging-v02.api.letsencrypt.org") {
			return c, errors.New("staging ACME certificates cannot serve console HTTPS; use certificate or proxy TLS mode while testing issuance")
		}
		if (c.CertificateFile == "") != (c.KeyFile == "") {
			return c, errors.New("provide both bootstrap TLS certificate and key")
		}
		if c.KeyFile != "" {
			if _, err = privateFile(c.KeyFile); err != nil {
				return c, err
			}
		}
	default:
		return c, errors.New("TLS mode must be certificate, acme, or proxy")
	}
	return c, c.Hosted.Validate()
}

func list(raw string) []string {
	out := []string{}
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/doout/dispatch/internal/tenancy"
)

func privateTestFile(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func configFixture(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISPATCH_HOSTED_DATA_DIR", root)
	t.Setenv("DISPATCH_HOSTED_ALLOW_SQLITE", "true")
	t.Setenv("DISPATCH_HOSTED_ROOT_DOMAIN", "dispatch.example.test")
	t.Setenv("DISPATCH_HOSTED_CONSOLE_ADDRESSES", "192.0.2.1")
	t.Setenv("DISPATCH_HOSTED_DNS_PROVIDER", "cloudflare")
	t.Setenv("DISPATCH_HOSTED_CLOUDFLARE_ZONE_ID", "0123456789abcdef0123456789abcdef")
	t.Setenv("DISPATCH_HOSTED_CLOUDFLARE_API_TOKEN_FILE", privateTestFile(t, "cloudflare-token", "private-test-cloudflare-token"))
	t.Setenv("DISPATCH_HOSTED_TLS_MODE", "proxy")
}
func TestHostedConfigurationRequiresExplicitPrivateInfrastructure(t *testing.T) {
	configFixture(t)
	if _, err := readConfiguration(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, value string }{
		{"DISPATCH_HOSTED_ADDR", "0.0.0.0:8081"},
		{"DISPATCH_HOSTED_CLOUDFLARE_ZONE_ID", ""},
		{"DISPATCH_HOSTED_ALLOW_SQLITE", "false"},
		{"DISPATCH_HOSTED_DATA_DIR", "relative"},
		{"DISPATCH_HOSTED_DNS_PROVIDER", "unknown"},
		{"DISPATCH_HOSTED_CLOUDFLARE_API_TOKEN_FILE", ""},
		{"DISPATCH_HOSTED_TRUSTED_PROXIES", "*"},
		{"DISPATCH_HOSTED_TLS_MODE", "acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.name, tc.value)
			if _, err := readConfiguration(); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}
func TestPrivateConfigurationFilesRejectSymlinksAndWorldRead(t *testing.T) {
	file := privateTestFile(t, "secret", "value")
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFile(file); err == nil {
		t.Fatal("public secret file accepted")
	}
	if err := os.Chmod(file, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if _, err := privateFile(link); err == nil {
		t.Fatal("secret symlink accepted")
	}
}
func TestBootstrapCreatesOnlyFirstAdministrator(t *testing.T) {
	configFixture(t)
	password := privateTestFile(t, "password", "a-long-initial-password")
	args := []string{"bootstrap-user", "--email", "admin@example.test", "--name", "Admin", "--password-file", password}
	var output bytes.Buffer
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run(context.Background(), args, &output, logger); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte("a-long-initial-password")) {
		t.Fatal("password printed")
	}
	if err := run(context.Background(), args, io.Discard, logger); err == nil {
		t.Fatal("repeated administrator bootstrap accepted")
	}
	_, database, err := catalogConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := tenancy.Open(context.Background(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	user, err := catalog.AuthenticatePassword(context.Background(), "admin@example.test", "a-long-initial-password")
	if err != nil || !user.PlatformAdmin {
		t.Fatal(user, err)
	}
	memberships, err := catalog.ListMemberships(context.Background(), user.ID)
	if err != nil || len(memberships) != 0 {
		t.Fatal(memberships, err)
	}
}
func TestOnlyOneControllerCanUseDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	release, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := lockDirectory(root); err == nil {
		other()
		t.Fatal("second controller acquired lock")
	}
	release()
	next, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestExplicitCatalogCannotOpenAnUnrelatedSQLiteFile(t *testing.T) {
	configFixture(t)
	t.Setenv("DISPATCH_HOSTED_CATALOG_URL_FILE", privateTestFile(t, "catalog-url", "/var/lib/dispatch/dispatch.db"))
	if _, _, err := catalogConfiguration(); err == nil {
		t.Fatal("explicit SQLite path bypassed hosted catalog boundary")
	}
}

func TestStagingIssuanceKeepsTrustedConsoleTLS(t *testing.T) {
	configFixture(t)
	t.Setenv("DISPATCH_HOSTED_ACME_DIRECTORY", "https://acme-staging-v02.api.letsencrypt.org/directory")
	t.Setenv("DISPATCH_HOSTED_ACME_EMAIL", "operations@example.test")
	t.Setenv("DISPATCH_HOSTED_ACME_ACCEPT_TERMS", "true")
	t.Setenv("DISPATCH_HOSTED_TLS_CERT_FILE", "/bootstrap/fullchain.pem")
	t.Setenv("DISPATCH_HOSTED_TLS_KEY_FILE", privateTestFile(t, "bootstrap-key", "fixture key"))
	for _, mode := range []string{"proxy", "certificate"} {
		t.Setenv("DISPATCH_HOSTED_TLS_MODE", mode)
		config, err := readConfiguration()
		if err != nil || config.TLSMode != mode || config.Hosted.Certificates.DirectoryURL == "" {
			t.Fatal("staging issuance cannot coexist with trusted console TLS", err)
		}
	}
	t.Setenv("DISPATCH_HOSTED_TLS_MODE", "acme")
	if _, err := readConfiguration(); err == nil {
		t.Fatal("staging issuance can replace trusted console TLS")
	}
	t.Setenv("DISPATCH_HOSTED_ACME_DIRECTORY", "https://acme-v02.api.letsencrypt.org/directory")
	if _, err := readConfiguration(); err != nil {
		t.Fatal("production ACME serving rejected", err)
	}
}

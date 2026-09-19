package serviceconn

import (
	"context"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/core"
)

func TestPostgresURLRoundTripAndValidation(t *testing.T) {
	input := "postgresql://user:p%40ss%3A%2F%3F%23@[::1]:5432/db%20name"
	fields, err := ParsePostgresURL(input)
	if err != nil {
		t.Fatal(err)
	}
	if fields["password"] != "p@ss:/?#" || fields["database"] != "db name" || fields["sslmode"] != "verify-full" {
		t.Fatalf("bad parsing: %#v", fields)
	}
	output, err := url.Parse(PostgresURL(fields))
	if err != nil {
		t.Fatal(err)
	}
	password, _ := output.User.Password()
	if password != fields["password"] {
		t.Fatal("password did not round trip")
	}
	for _, bad := range []string{"postgresql://user:secret@host/db?sslrootcert=/etc/file", "postgresql://host/db?sslmode=disable&sslmode=require", "http://host/db", "postgresql://host/db#fragment"} {
		if _, err := ParsePostgresURL(bad); err == nil {
			t.Fatalf("accepted unsupported URL %q", bad)
		}
	}
}
func TestServiceBindingDestinations(t *testing.T) {
	services := map[string]core.Service{"db": {Type: "generic", Fields: map[string]core.ServiceField{"url": {Configured: true}}}}
	valid := core.ServiceBinding{Alias: "db", ServiceRef: "db", Environment: map[string]string{"DATABASE_URL": "url"}}
	if err := ValidateBindings([]core.ServiceBinding{valid}, core.BuildTypeDockerfile, services); err != nil {
		t.Fatal(err)
	}
	other := valid
	other.Alias = "other"
	if err := ValidateBindings([]core.ServiceBinding{valid, other}, core.BuildTypeDockerfile, services); err == nil {
		t.Fatal("accepted duplicate destination")
	}
	valid.Environment["DATABASE_URL"] = "missing"
	if err := ValidateBindings([]core.ServiceBinding{valid}, core.BuildTypeDockerfile, services); err == nil {
		t.Fatal("accepted missing field")
	}
	helm := core.ServiceBinding{Alias: "db", ServiceRef: "db", Helm: &core.ServiceHelmBinding{Keys: map[string]string{"url": "url"}, SecretNameValues: []string{"database.secret", "database.secret.name"}}}
	if err := ValidateBindings([]core.ServiceBinding{helm}, core.BuildTypeHelm, services); err == nil {
		t.Fatal("accepted overlapping Helm destinations")
	}
}
func TestManualTCPCheckAndCancelledCheck(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	item := core.Service{Type: "generic", ProbeHost: "127.0.0.1", ProbePort: address.Port}
	result := (Resolver{}).Check(context.Background(), item)
	if result.State != "succeeded" || result.Location != "Dispatch controller" || result.CheckedAt.IsZero() {
		t.Fatalf("unexpected check: %#v", result)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result = (Resolver{}).Check(ctx, item)
	if result.State != "failed" || !strings.Contains(result.Message, "timed out") {
		t.Fatalf("unexpected timeout: %#v", result)
	}
	result = (Resolver{}).Check(context.Background(), core.Service{Type: "generic"})
	if result.State != "untested" {
		t.Fatal(result)
	}
}
func TestPostgresConnectionIntegration(t *testing.T) {
	raw := os.Getenv("DISPATCH_SERVICES_POSTGRES_URL")
	if raw == "" {
		t.Skip("set DISPATCH_SERVICES_POSTGRES_URL to a disposable PostgreSQL database")
	}
	fields, err := ParsePostgresURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := checkPostgres(ctx, fields); err != nil {
		t.Fatal(err)
	}
	fields["password"] = "incorrect-password"
	if err := checkPostgres(ctx, fields); err == nil {
		t.Fatal("accepted invalid credentials")
	}
	fields["sslmode"] = "verify-full"
	if err := checkPostgres(ctx, fields); err == nil {
		t.Fatal("accepted TLS against the plaintext test server")
	}
}

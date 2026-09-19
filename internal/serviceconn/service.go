// Package serviceconn validates and resolves connections without provisioning them.
package serviceconn

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
	secretcrypto "github.com/doout/dispatch/internal/crypto"
	"github.com/jackc/pgx/v5"
)

var NamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
var FieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
var envPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var valuePath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(\.[A-Za-z_][A-Za-z0-9_-]*)*$`)

type SecretResolver interface {
	Resolve(context.Context, string) ([]byte, error)
}
type Resolver struct {
	Vault   *secretcrypto.Vault
	Secrets SecretResolver
}

func FieldAAD(id, field string) string { return "service:" + id + ":" + field }

// ParsePostgresURL deliberately excludes driver options that can reference local
// files or change the endpoint behind the fields displayed in Dispatch.
func ParsePostgresURL(raw string) (map[string]string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() == "" || u.Fragment != "" {
		return nil, errors.New("enter a PostgreSQL connection URL")
	}
	fields := map[string]string{"host": u.Hostname(), "port": u.Port(), "database": strings.TrimPrefix(u.Path, "/"), "sslmode": "verify-full"}
	if u.User != nil {
		fields["username"] = u.User.Username()
		if p, ok := u.User.Password(); ok {
			fields["password"] = p
		}
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL URL options")
	}
	for key, values := range q {
		if key != "sslmode" || len(values) != 1 {
			return nil, errors.New("only sslmode is supported in connection URL options; supply CA certificates in caCert")
		}
		fields[key] = values[0]
	}
	return fields, nil
}

func Validate(s *core.Service) error {
	if !NamePattern.MatchString(s.Name) || s.ProjectID == "" {
		return errors.New("choose a project and a service name using lowercase letters, digits, dots or hyphens")
	}
	if s.Type != "postgresql" && s.Type != "generic" {
		return errors.New("choose PostgreSQL or generic")
	}
	if len(s.Fields) > 100 {
		return errors.New("a service accepts at most 100 fields")
	}
	for key, f := range s.Fields {
		if !FieldPattern.MatchString(key) || len(f.Value) > 65536 {
			return errors.New("invalid service field name or value length")
		}
		if f.Sensitive && f.Value != "" {
			return errors.New("sensitive values must be encrypted")
		}
		if f.SecretRef != "" && f.EncryptedValue != "" {
			return errors.New("choose a local credential or a secret reference")
		}
	}
	if s.Type == "postgresql" {
		allowed := map[string]bool{"host": true, "port": true, "database": true, "username": true, "password": true, "sslmode": true, "caCert": true}
		for key := range s.Fields {
			if !allowed[key] {
				return fmt.Errorf("unsupported PostgreSQL field %s", key)
			}
		}
		for key, def := range map[string]string{"port": "5432", "sslmode": "verify-full"} {
			if f, ok := s.Fields[key]; !ok || f.Value == "" && f.SecretRef == "" && f.EncryptedValue == "" {
				s.Fields[key] = core.ServiceField{Value: def, Configured: true}
			}
		}
		for _, key := range []string{"host", "port", "database", "username", "sslmode"} {
			f := s.Fields[key]
			if f.Value == "" && f.SecretRef == "" && f.EncryptedValue == "" {
				return fmt.Errorf("PostgreSQL requires %s", key)
			}
		}
		for _, key := range []string{"host", "port", "sslmode"} {
			f := s.Fields[key]
			if f.SecretRef != "" || f.Sensitive {
				return fmt.Errorf("%s must be an ordinary field", key)
			}
		}
		if err := validEndpoint(s.Fields["host"].Value, s.Fields["port"].Value); err != nil {
			return err
		}
		mode := s.Fields["sslmode"].Value
		if mode != "disable" && mode != "require" && mode != "verify-ca" && mode != "verify-full" {
			return errors.New("sslmode must be disable, require, verify-ca or verify-full")
		}
		if f, ok := s.Fields["password"]; ok && !f.Sensitive {
			return errors.New("password must be sensitive")
		}
	}
	if s.ProbeHost != "" || s.ProbePort != 0 {
		if err := validEndpoint(s.ProbeHost, strconv.Itoa(s.ProbePort)); err != nil {
			return err
		}
	}
	return nil
}
func validEndpoint(host, port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host == "" || strings.ContainsAny(host, "/\\ \t\r\n\x00") {
		return errors.New("enter a hostname and port between 1 and 65535")
	}
	return nil
}
func HasField(s core.Service, key string) bool {
	if s.Type == "postgresql" && key == "connectionUrl" {
		return true
	}
	f, ok := s.Fields[key]
	return ok && (f.Configured || f.Value != "" || f.EncryptedValue != "" || f.SecretRef != "")
}

func (r Resolver) Resolve(ctx context.Context, s core.Service) (map[string]string, error) {
	values := map[string]string{}
	for key, f := range s.Fields {
		switch {
		case f.CapturedSecretID != "":
			if r.Vault == nil {
				return nil, errors.New("service vault is unavailable")
			}
			b, err := r.Vault.Decrypt("secret:"+f.CapturedSecretID, f.CapturedSecretValue)
			if err != nil {
				return nil, fmt.Errorf("cannot decrypt captured service field %s", key)
			}
			values[key] = string(b)
			clear(b)
		case f.SecretRef != "":
			if r.Secrets == nil {
				return nil, errors.New("service secret resolver is unavailable")
			}
			b, err := r.Secrets.Resolve(ctx, f.SecretRef)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve service field %s", key)
			}
			values[key] = string(b)
			clear(b)
		case f.EncryptedValue != "":
			if r.Vault == nil {
				return nil, errors.New("service vault is unavailable")
			}
			b, err := r.Vault.Decrypt(FieldAAD(s.ID, key), f.EncryptedValue)
			if err != nil {
				return nil, fmt.Errorf("cannot decrypt service field %s", key)
			}
			values[key] = string(b)
			clear(b)
		default:
			values[key] = f.Value
		}
	}
	if s.Type == "postgresql" {
		values["connectionUrl"] = PostgresURL(values)
	}
	return values, nil
}
func PostgresURL(v map[string]string) string {
	u := url.URL{Scheme: "postgresql", Host: net.JoinHostPort(v["host"], v["port"]), Path: "/" + v["database"], User: url.UserPassword(v["username"], v["password"])}
	q := url.Values{"sslmode": {v["sslmode"]}}
	u.RawQuery = q.Encode()
	return u.String()
}

func (r Resolver) Check(ctx context.Context, s core.Service) core.ServiceCheck {
	start := time.Now()
	result := core.ServiceCheck{State: "failed", Message: "Connection failed. Check the endpoint, credentials, TLS settings and controller network access.", Location: "Dispatch controller", CheckedAt: start.UTC()}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	values, err := r.Resolve(ctx, s)
	if err == nil {
		if s.Type == "postgresql" {
			err = checkPostgres(ctx, values)
		} else if s.ProbeHost != "" {
			var c net.Conn
			c, err = (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(s.ProbeHost, strconv.Itoa(s.ProbePort)))
			if c != nil {
				c.Close()
			}
		} else {
			result.State = "untested"
			result.Message = "Configure a host and port to test TCP reachability."
		}
	}
	if err == nil && result.State != "untested" {
		result.State = "succeeded"
		result.Message = "Connection succeeded from the Dispatch controller."
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Message = "Connection test timed out after 10 seconds."
	}
	result.DurationMS = time.Since(start).Milliseconds()
	return result
}
func checkPostgres(ctx context.Context, v map[string]string) error {
	// A fully specified config prevents PG* environment variables from supplying
	// passwords, files, service definitions or alternate endpoints.
	cfg, err := pgx.ParseConfig("postgresql://localhost/postgres?sslmode=disable")
	if err != nil {
		return err
	}
	port, _ := strconv.Atoi(v["port"])
	cfg.Host = v["host"]
	cfg.Port = uint16(port)
	cfg.Database = v["database"]
	cfg.User = v["username"]
	cfg.Password = v["password"]
	cfg.Fallbacks = nil
	cfg.ConnectTimeout = 10 * time.Second
	cfg.RuntimeParams = map[string]string{}
	if v["sslmode"] != "disable" {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: v["host"]}
		if pem := v["caCert"]; pem != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(pem)) {
				return errors.New("invalid CA certificate")
			}
			tc.RootCAs = pool
		}
		switch v["sslmode"] {
		case "require":
			tc.InsecureSkipVerify = true
		case "verify-ca":
			tc.InsecureSkipVerify = true
			tc.VerifyConnection = func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("missing peer certificate")
				}
				pool := x509.NewCertPool()
				for _, c := range cs.PeerCertificates[1:] {
					pool.AddCert(c)
				}
				_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: tc.RootCAs, Intermediates: pool})
				return err
			}
		}
		cfg.TLSConfig = tc
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	var one int
	return conn.QueryRow(ctx, "SELECT 1").Scan(&one)
}

func ValidateBindings(bindings []core.ServiceBinding, build core.BuildType, services map[string]core.Service) error {
	aliases := map[string]bool{}
	destinations := []string{}
	add := func(dest string) error {
		for _, old := range destinations {
			if old == dest || strings.HasPrefix(old, dest+".") || strings.HasPrefix(dest, old+".") {
				return fmt.Errorf("duplicate or overlapping service destination %s", dest)
			}
		}
		destinations = append(destinations, dest)
		return nil
	}
	for _, b := range bindings {
		if !NamePattern.MatchString(b.Alias) || aliases[b.Alias] {
			return errors.New("service binding aliases must be valid and unique")
		}
		aliases[b.Alias] = true
		s, ok := services[b.ServiceRef]
		if !ok {
			return fmt.Errorf("service %s is unavailable in this project", b.ServiceRef)
		}
		field := func(key string) error {
			if !HasField(s, key) {
				return fmt.Errorf("service %s has no configured field %s", s.Name, key)
			}
			return nil
		}
		switch build {
		case core.BuildTypeDockerfile:
			if len(b.Environment) == 0 || len(b.Compose) > 0 || b.Helm != nil {
				return errors.New("Dockerfile bindings require environment mappings only")
			}
			for env, key := range b.Environment {
				if !envPattern.MatchString(env) {
					return errors.New("invalid environment variable name")
				}
				if err := field(key); err != nil {
					return err
				}
				if err := add(env); err != nil {
					return err
				}
			}
		case core.BuildTypeCompose:
			if len(b.Compose) == 0 || len(b.Environment) > 0 || b.Helm != nil {
				return errors.New("Compose bindings require explicit service/environment mappings")
			}
			for name, envs := range b.Compose {
				if name == "" || len(envs) == 0 {
					return errors.New("choose a Compose service and environment variables")
				}
				for env, key := range envs {
					if !envPattern.MatchString(env) {
						return errors.New("invalid environment variable name")
					}
					if err := field(key); err != nil {
						return err
					}
					if err := add(name + "/" + env); err != nil {
						return err
					}
				}
			}
		case core.BuildTypeHelm:
			if b.Helm == nil || len(b.Environment) > 0 || len(b.Compose) > 0 || len(b.Helm.Keys) == 0 || len(b.Helm.SecretNameValues) == 0 {
				return errors.New("Helm bindings require secret keys and chart value paths for the secret name")
			}
			for key, source := range b.Helm.Keys {
				if !FieldPattern.MatchString(key) {
					return errors.New("invalid Kubernetes Secret key")
				}
				if err := field(source); err != nil {
					return err
				}
			}
			for _, path := range b.Helm.SecretNameValues {
				if !valuePath.MatchString(path) {
					return errors.New("invalid Helm value path")
				}
				if err := add(path); err != nil {
					return err
				}
			}
			for path, key := range b.Helm.KeyValues {
				if !valuePath.MatchString(path) {
					return errors.New("invalid Helm value path")
				}
				if _, ok := b.Helm.Keys[key]; !ok {
					return errors.New("Helm key mapping references an undefined secret key")
				}
				if err := add(path); err != nil {
					return err
				}
			}
		default:
			return errors.New("unsupported service binding runtime")
		}
	}
	return nil
}

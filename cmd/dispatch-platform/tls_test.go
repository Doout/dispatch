package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/hosted"
)

func bootstrapCertificateFiles(t *testing.T, domain string, before, after time.Time) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{domain}, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, leaf, leaf, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "certificate.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestConsoleBootstrapCertificateIsLimitedToRootUntilManagedReady(t *testing.T) {
	root := "dispatch.example.test"
	certPath, keyPath := bootstrapCertificateFiles(t, root, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	unavailable := errors.New("certificate unavailable")
	var managed *tls.Certificate
	cfg, err := consoleTLSConfig(configuration{TLSMode: "acme", CertificateFile: certPath, KeyFile: keyPath, Hosted: hosted.Config{RootDomain: root}}, func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		if managed != nil {
			return managed, nil
		}
		return nil, unavailable
	})
	if err != nil {
		t.Fatal(err)
	}
	var fallback *tls.Certificate
	for _, host := range []string{root, strings.ToUpper(root)} {
		fallback, err = cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		if err != nil || fallback == nil || fallback.Leaf.VerifyHostname(root) != nil {
			t.Fatal("root console bootstrap failed", err)
		}
	}
	for _, host := range []string{"", "tenant." + root, "workload.tenant." + root, "foreign.test"} {
		if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: host}); !errors.Is(err, unavailable) {
			t.Fatalf("bootstrap certificate served for %q: %v", host, err)
		}
	}
	// The running listener must stop using a fallback that expires before
	// managed issuance recovers, without requiring a container restart.
	fallback.Leaf.NotAfter = time.Now().Add(-time.Minute)
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: root}); err == nil {
		t.Fatal("expired bootstrap certificate served")
	}
	managed = &tls.Certificate{}
	if got, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: root}); err != nil || got != managed {
		t.Fatal("managed certificate did not replace fallback", err)
	}
}

func TestConsoleRejectsUnusableBootstrapCertificate(t *testing.T) {
	root := "dispatch.example.test"
	now := time.Now()
	for _, tc := range []struct {
		name, host    string
		before, after time.Time
	}{
		{"wrong hostname", "other.example.test", now.Add(-time.Hour), now.Add(time.Hour)},
		{"expired", root, now.Add(-2 * time.Hour), now.Add(-time.Hour)},
		{"not yet valid", root, now.Add(time.Hour), now.Add(2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certPath, keyPath := bootstrapCertificateFiles(t, tc.host, tc.before, tc.after)
			_, err := consoleTLSConfig(configuration{TLSMode: "acme", CertificateFile: certPath, KeyFile: keyPath, Hosted: hosted.Config{RootDomain: root}}, nil)
			if err == nil {
				t.Fatal("unusable bootstrap certificate accepted")
			}
		})
	}
}

func TestConsoleWithoutBootstrapWaitsForManagedCertificate(t *testing.T) {
	unavailable := errors.New("certificate unavailable")
	cfg, err := consoleTLSConfig(configuration{TLSMode: "acme"}, func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
		return nil, unavailable
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "dispatch.example.test"}); !errors.Is(err, unavailable) {
		t.Fatal("missing managed certificate was hidden", err)
	}
}

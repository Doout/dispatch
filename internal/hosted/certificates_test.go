package hosted

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/doout/dispatch/internal/certificates"
	"github.com/doout/dispatch/internal/tenancy"
)

func hostedCertificateFixture(t *testing.T, domains ...string) certificates.CertificateResult {
	t.Helper()
	sort.Strings(domains)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: domains, NotBefore: now.Add(-70 * 24 * time.Hour), NotAfter: now.Add(20 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return certificates.CertificateResult{CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), NotAfter: cert.NotAfter, RenewAfter: cert.NotBefore.Add(cert.NotAfter.Sub(cert.NotBefore) * 2 / 3)}
}

func tlsFixtureHandshake(t *testing.T, server *Server, host string) (*x509.Certificate, error) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- tls.Server(serverSide, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: server.GetCertificate}).HandshakeContext(ctx)
	}()
	client := tls.Client(clientSide, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host, InsecureSkipVerify: true})
	err := client.HandshakeContext(ctx)
	if err != nil {
		clientSide.Close()
		<-done
		return nil, err
	}
	if err = <-done; err != nil {
		return nil, err
	}
	return client.ConnectionState().PeerCertificates[0], nil
}

func TestHostedTLSOnlyServesRegisteredControlPlaneHosts(t *testing.T) {
	f := newHostedFixture(t)
	f.tenant(t, "alpha", "owner-a")
	root := f.server.Config.RootDomain
	f.server.cacheCertificate(platformOwner, hostedCertificateFixture(t, root, "*."+root))
	for _, host := range []string{root, "alpha." + root} {
		cert, err := tlsFixtureHandshake(t, f.server, host)
		if err != nil {
			t.Fatal(host, err)
		}
		if err := cert.VerifyHostname(host); err != nil {
			t.Fatal(err)
		}
	}
	for _, host := range []string{"preview.alpha." + root, "unknown." + root, "foreign.test"} {
		if _, err := tlsFixtureHandshake(t, f.server, host); err == nil {
			t.Fatal("console served a workload or unregistered TLS name", host)
		}
	}
	expired := hostedCertificateFixture(t, root, "*."+root)
	expired.NotAfter = time.Now().Add(-time.Minute)
	f.server.cacheCertificate(platformOwner, expired)
	if _, err := tlsFixtureHandshake(t, f.server, root); err == nil {
		t.Fatal("expired certificate was served")
	}
}

func TestHostedTenantCertificateEndpointProtectsPrivateKeys(t *testing.T) {
	f := newHostedFixture(t)
	a := f.tenant(t, "alpha", "owner-a")
	b := f.tenant(t, "bravo", "owner-b")
	ctx := context.Background()
	if err := f.catalog.SetMembership(ctx, "owner-a", tenancy.Membership{TenantID: a.ID, UserID: "member", Role: tenancy.RoleMember}); err != nil {
		t.Fatal(err)
	}
	root := f.server.Config.RootDomain
	platform := hostedCertificateFixture(t, root, "*."+root)
	alpha := hostedCertificateFixture(t, "*.alpha."+root)
	bravo := hostedCertificateFixture(t, "*.bravo."+root)
	f.server.cacheCertificate(platformOwner, platform)
	f.server.cacheCertificate(a.ID, alpha)
	f.server.cacheCertificate(b.ID, bravo)
	host, path := "alpha."+root, "/api/v1/hosted/certificate"
	for _, token := range []string{f.session(t, "platform", tenancy.AudiencePlatform), f.session(t, "owner-b", tenancy.TenantAudience(b.ID)), f.session(t, "member", tenancy.TenantAudience(a.ID)), "old-dns-token", "worker-token", ""} {
		w := f.request(t, host, http.MethodGet, path, token, nil)
		if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), "PRIVATE KEY") {
			t.Fatal("certificate key disclosed to a nonowner", w.Code)
		}
	}
	owner := f.session(t, "owner-a", tenancy.TenantAudience(a.ID))
	w := f.request(t, host, http.MethodGet, path, owner, nil)
	var bundle struct {
		CertificatePEM string `json:"certificatePem"`
		PrivateKeyPEM  string `json:"privateKeyPem"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &bundle) != nil || bundle.PrivateKeyPEM != string(alpha.PrivateKeyPEM) || bundle.CertificatePEM != string(alpha.CertificatePEM) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("owner did not receive its private, uncached certificate bundle", w.Code)
	}
	if bundle.PrivateKeyPEM == string(platform.PrivateKeyPEM) || bundle.PrivateKeyPEM == string(bravo.PrivateKeyPEM) {
		t.Fatal("owner received another certificate key")
	}
	if w := f.request(t, host, http.MethodPost, path, owner, nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatal("certificate endpoint accepted a write", w.Code)
	}
	if err := f.catalog.RevokeSession(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if w := f.request(t, host, http.MethodGet, path, owner, nil); w.Code != http.StatusForbidden {
		t.Fatal("revoked session retrieved certificate")
	}
}

func TestHostedCertificateRestartLoadsValidBundleBeforeRenewal(t *testing.T) {
	f := newHostedFixture(t)
	root := f.server.Config.RootDomain
	result := hostedCertificateFixture(t, root, "*."+root)
	request := certificates.CertificateRequest{ID: root, OwnerID: platformOwner, Generation: 1, Domains: []string{"*." + root, root}}
	state := struct {
		DirectoryURL   string                          `json:"directoryUrl"`
		Request        certificates.CertificateRequest `json:"request"`
		CertificatePEM []byte                          `json:"certificatePem"`
		PrivateKeyPEM  []byte                          `json:"privateKeyPem"`
	}{"https://ca.example.test/directory", request, result.CertificatePEM, result.PrivateKeyPEM}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(f.server.Config.DataDirectory, "certificates")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(platformOwner + ":" + root))
	if err := os.WriteFile(filepath.Join(directory, hex.EncodeToString(digest[:])+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	f.server.Config.Certificates = CertificateConfig{DirectoryURL: "https://ca.example.test/directory", Email: "owner@example.test", TermsAccepted: true}
	// Cancellation makes renewal fail before any request reaches a CA. Loading
	// the saved certificate must still restore the console's TLS after restart.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.server.reconcileCertificate(ctx, nil); err == nil {
		t.Fatal("cancelled renewal succeeded")
	}
	if cert, err := tlsFixtureHandshake(t, f.server, root); err != nil || cert.VerifyHostname(root) != nil {
		t.Fatal("renewal failure hid a valid saved certificate", err)
	}
	loader := certificates.Reconciler{DirectoryURL: f.server.Config.Certificates.DirectoryURL, StoreDirectory: directory, Now: func() time.Time { return result.NotAfter.Add(time.Minute) }}
	if _, err := loader.Cached(request); err == nil {
		t.Fatal("expired saved certificate accepted")
	}
	loader.Now = nil
	request.Domains = []string{"*.foreign.test"}
	if _, err := loader.Cached(request); err == nil {
		t.Fatal("saved certificate used for different names")
	}
}

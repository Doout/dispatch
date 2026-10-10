package certificates

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/crypto/acme"
)

type fixtureCA struct {
	domains          []string
	now              time.Time
	orders, accepted int
	key              crypto.Signer
	malicious        bool
}

func (c *fixtureCA) Register(context.Context, *acme.Account, func(string) bool) (*acme.Account, error) {
	return &acme.Account{}, nil
}
func (c *fixtureCA) AuthorizeOrder(_ context.Context, ids []acme.AuthzID, _ ...acme.OrderOption) (*acme.Order, error) {
	c.orders++
	c.domains = nil
	for _, id := range ids {
		c.domains = append(c.domains, id.Value)
	}
	return &acme.Order{URI: "order", AuthzURLs: []string{"auth-1", "auth-2"}, FinalizeURL: "finalize"}, nil
}
func (c *fixtureCA) GetAuthorization(_ context.Context, id string) (*acme.Authorization, error) {
	name := "team.dispatch.example.test"
	if c.malicious {
		name = "foreign.example.test"
	}
	return &acme.Authorization{Status: acme.StatusPending, Identifier: acme.AuthzID{Type: "dns", Value: name}, Challenges: []*acme.Challenge{{Type: "dns-01", Token: id, URI: id}}}, nil
}
func (c *fixtureCA) DNS01ChallengeRecord(token string) (string, error) { return "proof-" + token, nil }
func (c *fixtureCA) Accept(_ context.Context, challenge *acme.Challenge) (*acme.Challenge, error) {
	c.accepted++
	return challenge, nil
}
func (c *fixtureCA) WaitAuthorization(context.Context, string) (*acme.Authorization, error) {
	return &acme.Authorization{Status: acme.StatusValid}, nil
}
func (c *fixtureCA) WaitOrder(context.Context, string) (*acme.Order, error) {
	return &acme.Order{URI: "order", FinalizeURL: "finalize", Status: acme.StatusReady}, nil
}
func (c *fixtureCA) CreateOrderCert(_ context.Context, _ string, raw []byte, _ bool) ([][]byte, string, error) {
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		return nil, "", err
	}
	if err = csr.CheckSignature(); err != nil {
		return nil, "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(int64(c.orders)), Subject: pkix.Name{CommonName: "Fixture"}, DNSNames: csr.DNSNames, NotBefore: c.now.Add(-time.Hour), NotAfter: c.now.Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, csr.PublicKey, key)
	return [][]byte{der}, "certificate", err
}

func TestACMEDNSIssuanceRenewalAndCleanup(t *testing.T) {
	stateDirectory := t.TempDir()
	solver := newFixtureSolver(t)
	ca := &fixtureCA{now: time.Now().UTC()}
	reconciler := Reconciler{DirectoryURL: "https://ca.example.test/directory", TermsAccepted: true, StoreDirectory: stateDirectory, Solver: solver, Now: func() time.Time { return ca.now }, Factory: func(key crypto.Signer) ACMEClient { ca.key = key; return ca }}
	request := CertificateRequest{ID: "tenant-console-and-workloads", OwnerID: "team", Generation: 1, Domains: []string{"team.dispatch.example.test", "*.team.dispatch.example.test"}}
	result, err := reconciler.Ensure(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.NotAfter.Before(ca.now) || solver.presented != 2 || solver.cleaned != 2 || ca.accepted != 2 {
		t.Fatalf("issuance did not finish: %+v", solver)
	}
	response := query(t, solver.address, "udp", "_acme-challenge.team.dispatch.example.test", dns.TypeTXT)
	if len(response.Answer) != 2 {
		t.Fatal("cleanup removed unrelated concurrent TXT values")
	}
	key := ca.key.Public()
	if _, err = reconciler.Ensure(context.Background(), request); err != nil || ca.orders != 1 {
		t.Fatalf("cached certificate was reordered: %v", err)
	}
	ca.now = result.RenewAfter.Add(time.Minute)
	if _, err = reconciler.Ensure(context.Background(), request); err != nil || ca.orders != 2 {
		t.Fatalf("certificate did not renew: %v", err)
	}
	if !key.(*ecdsa.PublicKey).Equal(ca.key.Public()) {
		t.Fatal("account key was not retained")
	}
	request.Generation = 2
	if _, err = reconciler.Ensure(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	request.Generation = 1
	if _, err = reconciler.Ensure(context.Background(), request); err == nil {
		t.Fatal("stale certificate generation accepted")
	}
	request.Generation = 2
	request.Domains = []string{"different.dispatch.example.test"}
	if _, err = reconciler.Ensure(context.Background(), request); err == nil {
		t.Fatal("same generation names changed")
	}
	entries, _ := os.ReadDir(stateDirectory)
	for _, entry := range entries {
		info, _ := entry.Info()
		if info.Mode().Perm() != 0600 {
			t.Errorf("private state mode %s: %v", entry.Name(), info.Mode())
		}
	}
}

func TestACMEWaitFailureAndForeignAuthorization(t *testing.T) {
	for _, malicious := range []bool{false, true} {
		t.Run(map[bool]string{false: "propagation", true: "foreign-authorization"}[malicious], func(t *testing.T) {
			solver := newFixtureSolver(t)
			solver.failWait = true
			ca := &fixtureCA{now: time.Now(), malicious: malicious}
			r := Reconciler{DirectoryURL: "https://ca.example.test/directory", TermsAccepted: true, StoreDirectory: t.TempDir(), Solver: solver, Factory: func(crypto.Signer) ACMEClient { return ca }}
			_, err := r.Ensure(context.Background(), CertificateRequest{ID: "certificate", OwnerID: "team", Generation: 1, Domains: []string{"*.team.dispatch.example.test"}})
			if err == nil || ca.accepted != 0 {
				t.Fatal("CA validation started before verified DNS")
			}
			if solver.presented != solver.cleaned {
				t.Fatal("failed challenge was not cleaned")
			}
			if malicious && solver.presented != 0 {
				t.Fatal("published outside authorized names")
			}
		})
	}
}

func TestACMEDirectorySwitchDoesNotReuseAnotherIssuersCertificate(t *testing.T) {
	solver := newFixtureSolver(t)
	staging := &fixtureCA{now: time.Now().UTC()}
	production := &fixtureCA{now: staging.now}
	r := Reconciler{DirectoryURL: "https://staging-ca.example.test/directory", TermsAccepted: true, StoreDirectory: t.TempDir(), Solver: solver, Now: func() time.Time { return staging.now }, Factory: func(crypto.Signer) ACMEClient { return staging }}
	request := CertificateRequest{ID: "workloads", OwnerID: "team", Generation: 1, Domains: []string{"*.team.dispatch.example.test"}}
	stagingCertificate, err := r.Ensure(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}

	r.DirectoryURL = "https://production-ca.example.test/directory"
	r.Factory = func(crypto.Signer) ACMEClient { return production }
	if _, err = r.Cached(request); err == nil {
		t.Fatal("production reconciler accepted a staging certificate")
	}
	// A production issuance failure must not relabel the saved staging bundle.
	solver.failWait = true
	if _, err = r.Ensure(t.Context(), request); err == nil {
		t.Fatal("production issuance succeeded before DNS propagation")
	}
	if _, err = r.Cached(request); err == nil {
		t.Fatal("failed production issuance made a staging certificate available")
	}
	solver.failWait = false
	certificate, err := r.Ensure(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if production.orders != 2 || bytes.Equal(certificate.CertificatePEM, stagingCertificate.CertificatePEM) {
		t.Fatal("production did not issue its own certificate")
	}
	cached, err := r.Cached(request)
	if err != nil || !bytes.Equal(cached.CertificatePEM, certificate.CertificatePEM) {
		t.Fatal("production certificate was not saved", err)
	}
	if _, err = r.Ensure(t.Context(), request); err != nil || production.orders != 2 {
		t.Fatal("matching production certificate was not reused", err)
	}

	// State written without the issuer identity must also be reissued.
	path := filepath.Join(r.StoreDirectory, digest(request.OwnerID+":"+request.ID)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	delete(state, "directoryUrl")
	raw, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = r.Cached(request); err == nil {
		t.Fatal("certificate without issuer identity was accepted")
	}
	if _, err = r.Ensure(t.Context(), request); err != nil || production.orders != 3 {
		t.Fatal("certificate without issuer identity was not reissued", err)
	}
}

func TestACMECleanupRetriesPersistedChallengesAfterRestart(t *testing.T) {
	solver := newFixtureSolver(t)
	solver.failCleanup = true
	ca := &fixtureCA{now: time.Now().UTC()}
	directory := t.TempDir()
	request := CertificateRequest{ID: "workloads", OwnerID: "team", Generation: 1, Domains: []string{"*.team.dispatch.example.test"}}
	reconciler := Reconciler{DirectoryURL: "https://ca.example.test/directory", TermsAccepted: true, StoreDirectory: directory, Solver: solver, Factory: func(crypto.Signer) ACMEClient { return ca }}
	if _, err := reconciler.Ensure(t.Context(), request); err == nil {
		t.Fatal("failed DNS cleanup was hidden")
	}
	path := filepath.Join(directory, digest(request.OwnerID+":"+request.ID)+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var pending certificateState
	if err := json.Unmarshal(raw, &pending); err != nil || len(pending.Challenges) != 2 {
		t.Fatal("failed cleanup was not retained on disk", err)
	}
	solver.failCleanup = false
	// A new reconciler must clean the saved challenges before using the certificate.
	reopened := Reconciler{DirectoryURL: reconciler.DirectoryURL, TermsAccepted: true, StoreDirectory: directory, Solver: solver, Factory: func(crypto.Signer) ACMEClient { t.Fatal("restart ordered another certificate"); return ca }}
	certificate, err := reopened.Ensure(t.Context(), request)
	if err != nil || certificate.NotAfter.Before(ca.now) {
		t.Fatal("restart did not recover the issued certificate", err)
	}
	if ca.orders != 1 {
		t.Fatal("restart created another order")
	}
	for _, network := range []string{"udp", "tcp"} {
		if got := query(t, solver.address, network, "_acme-challenge.team.dispatch.example.test", dns.TypeTXT); len(got.Answer) != 2 {
			t.Fatal("cleanup changed unrelated TXT values", network)
		}
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pending = certificateState{}
	if err := json.Unmarshal(raw, &pending); err != nil || len(pending.Challenges) != 0 {
		t.Fatal("successful cleanup was not saved", err)
	}
}

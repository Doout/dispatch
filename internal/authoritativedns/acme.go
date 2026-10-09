package authoritativedns

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme"
)

type Challenge struct {
	ID         string `json:"id"`
	OwnerID    string `json:"ownerId"`
	Generation uint64 `json:"generation"`
	Name       string `json:"name"`
	Value      string `json:"value"`
}

// ChallengeSolver must store each challenge separately, authorize its owner,
// and remove only that exact ID/value/generation. Wait confirms all advertised
// authoritative replicas serve the value before the CA is asked to validate it.
type ChallengeSolver interface {
	Present(context.Context, Challenge) error
	Wait(context.Context, Challenge) error
	Cleanup(context.Context, Challenge) error
}

type CertificateRequest struct {
	ID         string   `json:"id"`
	OwnerID    string   `json:"ownerId"`
	Generation uint64   `json:"generation"`
	Domains    []string `json:"domains"`
}

type CertificateResult struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	NotAfter       time.Time
	RenewAfter     time.Time
}

// ACMEClient is implemented by x/crypto/acme.Client. Factory is injectable for
// offline CA fixtures; normal callers leave it nil.
type ACMEClient interface {
	Register(context.Context, *acme.Account, func(string) bool) (*acme.Account, error)
	AuthorizeOrder(context.Context, []acme.AuthzID, ...acme.OrderOption) (*acme.Order, error)
	GetAuthorization(context.Context, string) (*acme.Authorization, error)
	DNS01ChallengeRecord(string) (string, error)
	Accept(context.Context, *acme.Challenge) (*acme.Challenge, error)
	WaitAuthorization(context.Context, string) (*acme.Authorization, error)
	WaitOrder(context.Context, string) (*acme.Order, error)
	CreateOrderCert(context.Context, string, []byte, bool) ([][]byte, string, error)
}

type Reconciler struct {
	DirectoryURL   string
	Email          string
	TermsAccepted  bool
	StoreDirectory string
	Solver         ChallengeSolver
	Factory        func(crypto.Signer) ACMEClient
	Now            func() time.Time
}

type certificateState struct {
	Request        CertificateRequest `json:"request"`
	CertificatePEM []byte             `json:"certificatePem,omitempty"`
	PrivateKeyPEM  []byte             `json:"privateKeyPem,omitempty"`
	Challenges     []Challenge        `json:"challenges,omitempty"`
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func lock(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
		}
		if err != syscall.EWOULDBLOCK {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func keyPEM() ([]byte, crypto.Signer, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), key, nil
}

func accountKey(ctx context.Context, path string) (crypto.Signer, error) {
	unlock, err := lock(ctx, path+".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		encoded, key, e := keyPEM()
		if e != nil {
			return nil, e
		}
		return key, atomicWrite(path, encoded)
	}
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("invalid saved ACME account key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("invalid saved ACME signer")
	}
	return signer, nil
}

// Ensure returns a matching valid certificate, renewing after two thirds of
// its lifetime. The caller supplies only catalog-authorized DNS names and runs
// Ensure periodically. No production directory is selected implicitly.
func (r *Reconciler) Ensure(ctx context.Context, request CertificateRequest) (result CertificateResult, err error) {
	if r.Solver == nil || !filepath.IsAbs(r.StoreDirectory) || !r.TermsAccepted || request.ID == "" || request.OwnerID == "" || request.Generation == 0 || len(request.Domains) == 0 || len(request.Domains) > 100 {
		return result, errors.New("ACME requires approved terms, solver, absolute state directory, and an owned certificate request")
	}
	u, e := url.Parse(r.DirectoryURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return result, errors.New("ACME directory must use HTTPS")
	}
	request, e = normalizeCertificateRequest(request)
	if e != nil {
		return result, e
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	path := filepath.Join(r.StoreDirectory, digest(request.OwnerID+":"+request.ID)+".json")
	unlock, e := lock(ctx, path+".lock")
	if e != nil {
		return result, e
	}
	defer unlock()
	state := certificateState{}
	raw, e := os.ReadFile(path)
	if e == nil {
		if e = json.Unmarshal(raw, &state); e != nil {
			return result, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, e
	}
	if state.Request.ID != "" {
		if state.Request.OwnerID != request.OwnerID || state.Request.ID != request.ID || request.Generation < state.Request.Generation {
			return result, errors.New("stale certificate ownership")
		}
		if request.Generation == state.Request.Generation && !reflect.DeepEqual(state.Request.Domains, request.Domains) {
			return result, errors.New("certificate generation already has different domains")
		}
	}
	save := func() error {
		raw, e := json.Marshal(state)
		if e != nil {
			return e
		}
		return atomicWrite(path, raw)
	}
	cleanup := func() error {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		remaining := []Challenge{}
		var joined error
		for _, challenge := range state.Challenges {
			if e := r.Solver.Cleanup(cleanupCtx, challenge); e != nil {
				remaining = append(remaining, challenge)
				joined = errors.Join(joined, e)
			}
		}
		state.Challenges = remaining
		return errors.Join(joined, save())
	}
	if len(state.Challenges) > 0 {
		if e = cleanup(); e != nil {
			return result, e
		}
	}
	state.Request = request
	if e = save(); e != nil {
		return result, e
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if cached, e := inspectCertificate(state.CertificatePEM, state.PrivateKeyPEM, request.Domains, now); e == nil && now.Before(cached.RenewAfter) {
		return cached, nil
	}
	signer, e := accountKey(ctx, filepath.Join(r.StoreDirectory, "account-"+digest(r.DirectoryURL)+".pem"))
	if e != nil {
		return result, e
	}
	var client ACMEClient = &acme.Client{Key: signer, DirectoryURL: r.DirectoryURL, HTTPClient: &http.Client{Timeout: 30 * time.Second}}
	if r.Factory != nil {
		client = r.Factory(signer)
	}
	contacts := []string{}
	if r.Email != "" {
		contacts = append(contacts, "mailto:"+r.Email)
	}
	if _, e = client.Register(ctx, &acme.Account{Contact: contacts}, func(string) bool { return r.TermsAccepted }); e != nil && !errors.Is(e, acme.ErrAccountAlreadyExists) {
		return result, e
	}
	order, e := client.AuthorizeOrder(ctx, acme.DomainIDs(request.Domains...))
	if e != nil {
		return result, e
	}
	defer func() {
		if len(state.Challenges) > 0 {
			err = errors.Join(err, cleanup())
		}
	}()
	for _, authURL := range order.AuthzURLs {
		auth, e := client.GetAuthorization(ctx, authURL)
		if e != nil {
			return result, e
		}
		if auth.Status == acme.StatusValid {
			continue
		}
		var selected *acme.Challenge
		for _, challenge := range auth.Challenges {
			if challenge.Type == "dns-01" {
				selected = challenge
				break
			}
		}
		if selected == nil {
			return result, errors.New("CA offered no DNS-01 challenge")
		}
		value, e := client.DNS01ChallengeRecord(selected.Token)
		if e != nil {
			return result, e
		}
		name, e := canonical("_acme-challenge."+strings.TrimPrefix(auth.Identifier.Value, "*."), false)
		if e != nil {
			return result, e
		}
		// Reject a CA response outside the requested set before publishing DNS.
		matched := false
		for _, domain := range request.Domains {
			if strings.TrimPrefix(domain, "*.") == strings.TrimPrefix(auth.Identifier.Value, "*.") {
				matched = true
				break
			}
		}
		if !matched {
			return result, errors.New("ACME authorization is outside the requested domains")
		}
		challenge := Challenge{ID: digest(order.URI + ":" + authURL + ":" + value), OwnerID: request.OwnerID, Generation: request.Generation, Name: name, Value: value}
		state.Challenges = append(state.Challenges, challenge)
		if e = save(); e != nil {
			return result, e
		}
		if e = r.Solver.Present(ctx, challenge); e != nil {
			return result, e
		}
		if e = r.Solver.Wait(ctx, challenge); e != nil {
			return result, e
		}
		if _, e = client.Accept(ctx, selected); e != nil {
			return result, e
		}
		if _, e = client.WaitAuthorization(ctx, authURL); e != nil {
			return result, e
		}
	}
	order, e = client.WaitOrder(ctx, order.URI)
	if e != nil {
		return result, e
	}
	encoded, key, e := keyPEM()
	if e != nil {
		return result, e
	}
	csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{DNSNames: request.Domains}, key)
	if e != nil {
		return result, e
	}
	chain, _, e := client.CreateOrderCert(ctx, order.FinalizeURL, csr, true)
	if e != nil {
		return result, e
	}
	certificate := []byte{}
	for _, der := range chain {
		certificate = append(certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	result, e = inspectCertificate(certificate, encoded, request.Domains, now)
	if e != nil {
		return result, e
	}
	state.CertificatePEM = certificate
	state.PrivateKeyPEM = encoded
	if e = save(); e != nil {
		return CertificateResult{}, e
	}
	return result, nil
}

// Cached loads a valid owned certificate without contacting the CA or changing
// DNS. Gateways call it on startup so a renewal outage cannot hide a certificate
// that is still valid on disk. Renewal is still the caller's responsibility.
func (r *Reconciler) Cached(request CertificateRequest) (CertificateResult, error) {
	if !filepath.IsAbs(r.StoreDirectory) {
		return CertificateResult{}, errors.New("certificate state directory must be absolute")
	}
	request, err := normalizeCertificateRequest(request)
	if err != nil {
		return CertificateResult{}, err
	}
	raw, err := os.ReadFile(filepath.Join(r.StoreDirectory, digest(request.OwnerID+":"+request.ID)+".json"))
	if err != nil {
		return CertificateResult{}, err
	}
	var state certificateState
	if err = json.Unmarshal(raw, &state); err != nil {
		return CertificateResult{}, err
	}
	if state.Request.ID != request.ID || state.Request.OwnerID != request.OwnerID || request.Generation < state.Request.Generation || !reflect.DeepEqual(state.Request.Domains, request.Domains) {
		return CertificateResult{}, errors.New("saved certificate ownership or names do not match")
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	return inspectCertificate(state.CertificatePEM, state.PrivateKeyPEM, request.Domains, now)
}

func normalizeCertificateRequest(request CertificateRequest) (CertificateRequest, error) {
	if request.ID == "" || request.OwnerID == "" || request.Generation == 0 || len(request.Domains) == 0 || len(request.Domains) > 100 {
		return request, errors.New("certificate request requires ownership, generation, and DNS names")
	}
	request.Domains = append([]string(nil), request.Domains...)
	for i, domain := range request.Domains {
		name, err := canonical(domain, true)
		if err != nil {
			return request, err
		}
		request.Domains[i] = strings.TrimSuffix(name, ".")
	}
	sort.Strings(request.Domains)
	for i := 1; i < len(request.Domains); i++ {
		if request.Domains[i] == request.Domains[i-1] {
			return request, errors.New("duplicate certificate domain")
		}
	}
	return request, nil
}

func inspectCertificate(cert, key []byte, domains []string, now time.Time) (CertificateResult, error) {
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return CertificateResult{}, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return CertificateResult{}, err
	}
	got := append([]string(nil), leaf.DNSNames...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, domains) || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return CertificateResult{}, errors.New("certificate names or validity do not match")
	}
	renew := leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3)
	return CertificateResult{CertificatePEM: cert, PrivateKeyPEM: key, NotAfter: leaf.NotAfter, RenewAfter: renew}, nil
}

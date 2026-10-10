package certificatesync

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixture struct {
	config  Config
	value   bundle
	ca      *x509.Certificate
	key     *ecdsa.PrivateKey
	reloads int
	status  int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{status: http.StatusOK}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f.key = key
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour)}
	raw, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	f.ca, err = x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(f.ca)
	directory := t.TempDir()
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte(strings.Repeat("secret", 8)), 0600); err != nil {
		t.Fatal(err)
	}
	f.config = Config{Origin: "https://alpha.dispatch.example.test", TokenFile: tokenFile, Directory: filepath.Join(directory, "certificates"), Roots: roots}
	f.config.Reload = func(context.Context) error { f.reloads++; return nil }
	f.config.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != f.config.Origin+"/api/v1/hosted/certificate" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("secret", 8) {
			t.Fatal("wrong certificate request")
		}
		body, err := json.Marshal(f.value)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader(string(body))), Header: http.Header{}}, nil
	})}
	f.value = f.certificate(t, []string{"*.alpha.dispatch.example.test"}, time.Now().Add(time.Hour))
	return f
}
func (f *fixture) certificate(t *testing.T, domains []string, expires time.Time) bundle {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: domains, NotBefore: time.Now().Add(-time.Hour), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	raw, err := x509.CreateCertificate(rand.Reader, cert, f.ca, key.Public(), f.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return bundle{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})), string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))}
}
func active(t *testing.T, f *fixture) bundle {
	t.Helper()
	value, err := readBundle(filepath.Join(f.config.Directory, "current"))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSyncInstallsAtomicallyAndReloadsOnlyChanges(t *testing.T) {
	f := newFixture(t)
	for index := 0; index < 3; index++ {
		if index != 0 {
			f.value = f.certificate(t, []string{"*.alpha.dispatch.example.test"}, time.Now().Add(time.Hour))
		}
		changed, err := Sync(context.Background(), f.config)
		if err != nil || !changed || active(t, f) != f.value {
			t.Fatal(changed, err)
		}
		if changed, err := Sync(context.Background(), f.config); err != nil || changed {
			t.Fatal("unchanged bundle reloaded", changed, err)
		}
	}
	if f.reloads != 3 {
		t.Fatal(f.reloads)
	}
	entries, err := os.ReadDir(f.config.Directory)
	if err != nil {
		t.Fatal(err)
	}
	generations := 0
	for _, entry := range entries {
		if entry.IsDir() {
			generations++
		}
	}
	if generations != 2 {
		t.Fatal("old certificate keys not removed", generations)
	}
	for _, file := range []string{"fullchain.pem", "privkey.pem"} {
		info, err := os.Stat(filepath.Join(f.config.Directory, "current", file))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("public certificate file", err)
		}
	}
}

func TestSyncRetainsLastBundleOnRejectedDownload(t *testing.T) {
	for _, kind := range []string{"foreign tenant", "platform bundle", "expired", "wrong key", "untrusted", "unavailable", "redirect"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t)
			if _, err := Sync(context.Background(), f.config); err != nil {
				t.Fatal(err)
			}
			old := f.value
			switch kind {
			case "foreign tenant":
				f.value = f.certificate(t, []string{"*.bravo.dispatch.example.test"}, time.Now().Add(time.Hour))
			case "platform bundle":
				f.value = f.certificate(t, []string{"dispatch.example.test", "*.dispatch.example.test"}, time.Now().Add(time.Hour))
			case "expired":
				f.value = f.certificate(t, []string{"*.alpha.dispatch.example.test"}, time.Now().Add(-time.Minute))
			case "wrong key":
				f.value.PrivateKeyPEM = f.certificate(t, []string{"*.alpha.dispatch.example.test"}, time.Now().Add(time.Hour)).PrivateKeyPEM
			case "untrusted":
				f.config.Roots = x509.NewCertPool()
			case "unavailable":
				f.status = http.StatusServiceUnavailable
			case "redirect":
				f.config.Client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://foreign.test/certificate"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
				})}
			}
			if _, err := Sync(context.Background(), f.config); err == nil {
				t.Fatal("bad bundle accepted")
			}
			if active(t, f) != old || f.reloads != 1 {
				t.Fatal("working certificate replaced")
			}
		})
	}
}

func TestSyncRollsBackFailedReload(t *testing.T) {
	f := newFixture(t)
	if _, err := Sync(context.Background(), f.config); err != nil {
		t.Fatal(err)
	}
	old := f.value
	f.value = f.certificate(t, []string{"*.alpha.dispatch.example.test"}, time.Now().Add(time.Hour))
	calls := 0
	f.config.Reload = func(ctx context.Context) error {
		calls++
		if calls == 1 {
			if active(t, f) != f.value {
				t.Fatal("new key and certificate were not selected together")
			}
			return errors.New("nginx failed")
		}
		if active(t, f) != old {
			t.Fatal("old bundle was not restored before reload")
		}
		return nil
	}
	if _, err := Sync(context.Background(), f.config); err == nil {
		t.Fatal("failed reload succeeded")
	}
	if calls != 2 || active(t, f) != old {
		t.Fatal("rollback failed")
	}
}

func TestSyncRetriesReloadAfterInterruptedInstallation(t *testing.T) {
	f := newFixture(t)
	if _, err := Sync(context.Background(), f.config); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.config.Directory, "current", "activated")); err != nil {
		t.Fatal(err)
	}
	if changed, err := Sync(context.Background(), f.config); err != nil || !changed || f.reloads != 2 {
		t.Fatal("interrupted reload not retried", changed, err)
	}
}

func TestSyncRejectsPublicAndSymlinkCredentials(t *testing.T) {
	f := newFixture(t)
	if err := os.Chmod(f.config.TokenFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Sync(context.Background(), f.config); err == nil {
		t.Fatal("public token accepted")
	}
	if err := os.Chmod(f.config.TokenFile, 0600); err != nil {
		t.Fatal(err)
	}
	link := f.config.TokenFile + "-link"
	if err := os.Symlink(f.config.TokenFile, link); err != nil {
		t.Fatal(err)
	}
	f.config.TokenFile = link
	if _, err := Sync(context.Background(), f.config); err == nil {
		t.Fatal("symlink token accepted")
	}
}

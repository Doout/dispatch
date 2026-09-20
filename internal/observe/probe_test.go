package observe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProbeRejectsUnsafeDestinations(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "http://localhost/", "http://169.254.169.254/", "http://[::1]/", "https://user:pass@example.com/", "https://example.com/?token=secret", "http://10.0.0.1/", "http://100.100.100.200/", "http://[::ffff:127.0.0.1]/"} {
		if _, err := validateURL(raw, false); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "2002:7f00:0001::1"} {
		if publicAddress(netip.MustParseAddr(ip)) {
			t.Error(ip)
		}
	}
	if _, err := validateURL("https://app.example.com/health", false); err != nil {
		t.Fatal(err)
	}
	if _, err := validateURL("http://hooks.example.com", true); err == nil {
		t.Fatal("webhook accepted HTTP")
	}
}
func TestProbeSeparatesTLSHTTPAndTimeoutWithoutSecrets(t *testing.T) {
	expiry := time.Now().Add(24 * time.Hour)
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("credentials forwarded")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("sensitive-body")), TLS: &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{NotAfter: expiry}}}}, nil
	})}
	result := probeEndpoint(context.Background(), "https://example.com/health", client)
	if result.State != "reachable" || result.TLS != "verified" || result.CertificateExpiresAt == nil || strings.Contains(result.Message, "sensitive-body") {
		t.Fatal(result)
	}
	for _, test := range []struct {
		err        error
		state, tls string
	}{{context.DeadlineExceeded, "unreachable", "not_checked"}, {&tls.CertificateVerificationError{Err: errors.New("private certificate details")}, "tls_failed", "invalid"}} {
		client.Transport = roundTrip(func(*http.Request) (*http.Response, error) { return nil, test.err })
		result = probeEndpoint(context.Background(), "https://example.com", client)
		if result.State != test.state || result.TLS != test.tls || strings.Contains(result.Message, "private certificate details") {
			t.Fatal(result)
		}
	}
}
func TestProbeDoesNotFollowRedirects(t *testing.T) {
	client := safeClient()
	calls := 0
	client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"http://169.254.169.254/secret"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	result := probeEndpoint(context.Background(), "https://example.com", client)
	if calls != 1 || result.State != "redirect" {
		t.Fatal(result, calls)
	}
}

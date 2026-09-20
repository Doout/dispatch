package observe

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// Endpoint checks are public reachability checks from the controller. Never use
// the controller's cookies, proxies or credentials, or follow a redirect to a
// different network. DNS results are checked again for every connection.
func validateURL(raw string, webhook bool) (*url.URL, error) {
	if len(raw) > 4096 {
		return nil, errors.New("URL is too long")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("Enter an absolute URL without credentials or a fragment")
	}
	if u.Scheme != "https" && (webhook || u.Scheme != "http") {
		return nil, errors.New("Use HTTPS for notifications, or HTTP/HTTPS for endpoint checks")
	}
	if !webhook && u.RawQuery != "" {
		return nil, errors.New("Endpoint checks do not accept query strings or credentials")
	}
	if port := u.Port(); port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("Invalid URL port")
		}
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return nil, errors.New("Checks and webhooks require a public network destination")
	}
	if ip, err := netip.ParseAddr(host); err == nil && !publicAddress(ip) {
		return nil, errors.New("Checks and webhooks require a public network destination")
	}
	return u, nil
}
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	// Cloud metadata, documentation, carrier-grade NAT, transition and special use
	// addresses are not public application endpoints.
	for _, prefix := range []string{"0.0.0.0/8", "192.88.99.0/24", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "64:ff9b:1::/48", "2001::/32", "2001:db8::/32", "2002::/16"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return false
		}
	}
	return true
}
func safeClient() *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("Invalid destination")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("Destination name could not be resolved")
		}
		for _, ip := range ips {
			if !publicAddress(ip) {
				return nil, errors.New("Destination is not public")
			}
		}
		var last error
		for _, ip := range ips {
			conn, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}
	return &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}
func ProbeEndpoint(ctx context.Context, raw string) core.EndpointObservation {
	return probeEndpoint(ctx, raw, safeClient())
}
func probeEndpoint(ctx context.Context, raw string, client *http.Client) core.EndpointObservation {
	result := core.EndpointObservation{State: "not_configured", TLS: "not_checked", Location: "Dispatch controller", Message: "No endpoint check is configured."}
	if raw == "" {
		return result
	}
	result.State = "unreachable"
	result.Message = "The endpoint could not be reached from the Dispatch controller."
	u, err := validateURL(raw, false)
	if err != nil {
		result.Message = "The configured endpoint is not a permitted public URL."
		return result
	}
	if u.Scheme == "http" {
		result.TLS = "not_applicable"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return result
	}
	req.Header.Set("User-Agent", "Dispatch-Observation/1")
	req.Header.Set("Range", "bytes=0-65535")
	res, err := client.Do(req)
	result.DurationMS = time.Since(started).Milliseconds()
	if err != nil {
		var cert *tls.CertificateVerificationError
		var unknown x509.UnknownAuthorityError
		var hostname x509.HostnameError
		if errors.As(err, &cert) || errors.As(err, &unknown) || errors.As(err, &hostname) {
			result.State = "tls_failed"
			result.TLS = "invalid"
			result.Message = "TLS certificate validation failed. Check the certificate chain, hostname and expiry."
		} else if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			result.Message = "The endpoint did not respond within ten seconds."
		}
		return result
	}
	defer res.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	result.DurationMS = time.Since(started).Milliseconds()
	result.HTTPStatus = res.StatusCode
	if res.TLS != nil {
		result.TLS = "verified"
		if len(res.TLS.PeerCertificates) > 0 {
			expiry := res.TLS.PeerCertificates[0].NotAfter
			result.CertificateExpiresAt = &expiry
		}
	}
	switch {
	case res.StatusCode >= 200 && res.StatusCode < 300:
		result.State = "reachable"
		result.Message = "The endpoint responded successfully from the Dispatch controller."
	case res.StatusCode >= 300 && res.StatusCode < 400:
		result.State = "redirect"
		result.Message = "The endpoint returned a redirect. Configure the final URL; redirects are not followed."
	default:
		result.State = "http_error"
		result.Message = "The endpoint responded with an unsuccessful HTTP status."
	}
	return result
}
func DeliverWebhook(ctx context.Context, raw string, event core.ObservationEvent) error {
	u, err := validateURL(raw, true)
	if err != nil {
		return errors.New("Notification destination is not permitted")
	}
	payload := struct {
		ID            string    `json:"id"`
		AppID         string    `json:"appId"`
		ProjectID     string    `json:"projectId"`
		DeploymentID  string    `json:"deploymentId,omitempty"`
		Kind          string    `json:"kind"`
		State         string    `json:"state"`
		PreviousState string    `json:"previousState,omitempty"`
		Message       string    `json:"message"`
		Link          string    `json:"link"`
		At            time.Time `json:"createdAt"`
	}{event.ID, event.AppID, event.ProjectID, event.DeploymentID, event.Kind, event.State, event.PreviousState, event.Message, event.Link, event.CreatedAt}
	rawPayload, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(rawPayload))
	if err != nil {
		return errors.New("Notification request could not be prepared")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", event.ID)
	req.Header.Set("User-Agent", "Dispatch-Observation/1")
	response, err := safeClient().Do(req)
	if err != nil {
		return errors.New("Notification delivery failed")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return errors.New("Notification endpoint did not accept the event")
	}
	return nil
}

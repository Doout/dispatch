package routing

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/doout/dispatch/internal/core"
)

// Probe reports only public evidence; publication is not proof that DNS,
// Traefik reload or ACME issuance has completed. No response body is retained.
type Probe struct {
	LookupHost func(context.Context, string) ([]string, error)
	Client     *http.Client
	Now        func() time.Time
}

func (p Probe) Check(ctx context.Context, route core.ApplicationRoute) core.ApplicationRoute {
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	route.UpdatedAt = now
	lookup := p.LookupHost
	if lookup == nil {
		lookup = net.DefaultResolver.LookupHost
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addresses, err := lookup(ctx, route.Hostname)
	if err != nil || len(addresses) == 0 {
		route.DNS = "pending"
		route.State = "pending"
		route.Message = "Hostname does not resolve yet. Point its DNS record at the target proxy."
		return route
	}
	route.DNS = "resolved"
	scheme := "http"
	if route.RequireTLS {
		scheme = "https"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+route.Hostname+"/", nil)
	if err != nil {
		route.State = "failed"
		route.Message = "The route hostname is invalid."
		return route
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10, DisableKeepAlives: true}}
	}
	response, err := client.Do(request)
	route.Certificate.CheckedAt = &now
	if err != nil {
		route.State = "pending"
		route.Message = "Public route is not reachable yet. Check proxy configuration and DNS."
		if route.RequireTLS {
			route.Certificate.State = "pending"
			route.Certificate.Message = "Waiting for the proxy to issue and serve a trusted certificate."
			var invalid *tls.CertificateVerificationError
			if errors.As(err, &invalid) {
				route.Certificate.State = "invalid"
				route.Certificate.Message = "Certificate verification failed. Check its hostname, chain and expiry."
			}
		}
		return route
	}
	defer response.Body.Close()
	if route.RequireTLS {
		if response.TLS == nil || len(response.TLS.VerifiedChains) == 0 || len(response.TLS.PeerCertificates) == 0 {
			route.State = "pending"
			route.Certificate.State = "invalid"
			route.Certificate.Message = "The public endpoint did not provide verified TLS evidence."
			return route
		}
		expiry := response.TLS.PeerCertificates[0].NotAfter
		route.Certificate.ExpiresAt = &expiry
		route.Certificate.State = "verified"
		route.Certificate.Message = "The public certificate is valid for this hostname."
		if expiry.Before(now.Add(7 * 24 * time.Hour)) {
			route.Certificate.State = "renewal_due"
			route.Certificate.Message = "The certificate expires within seven days; check ACME renewal."
		}
	}
	if route.DeploymentID == "" {
		route.State = "pending"
		route.Message = "Hostname and certificate are prepared; no healthy candidate has been published."
		return route
	}
	if response.Header.Get("X-Dispatch-Deployment") != route.DeploymentID {
		route.State = "pending"
		route.Message = "The proxy has not confirmed the active destination. Check the file provider reload and DNS target."
		return route
	}
	route.State = "active"
	route.Message = "Public route and required certificate are verified."
	return route
}

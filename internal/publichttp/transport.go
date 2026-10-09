// Package publichttp confines hosted HTTP integrations to public network
// destinations. Private services must use a tenant's enrolled relay.
package publichttp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"time"
)

var excluded = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("fec0::/10"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range excluded {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

type dialPolicy struct {
	resolve func(context.Context, string, string) ([]netip.Addr, error)
	dial    func(context.Context, string, string) (net.Conn, error)
}

func (p dialPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid integration address")
	}
	addresses, err := p.resolve(ctx, "ip", host)
	if err != nil {
		return nil, errors.New("cannot resolve integration host")
	}
	if len(addresses) == 0 {
		return nil, errors.New("integration host has no addresses")
	}
	for _, ip := range addresses {
		if !public(ip) {
			return nil, errors.New("private integration addresses require an enrolled relay")
		}
	}
	for _, ip := range addresses {
		conn, err := p.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, errors.New("cannot connect to integration host")
}

func Transport() *http.Transport {
	dialer := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	policy := dialPolicy{resolve: net.DefaultResolver.LookupNetIP, dial: dialer.DialContext}
	return &http.Transport{Proxy: nil, DialContext: policy.DialContext, ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, ExpectContinueTimeout: time.Second, MaxResponseHeaderBytes: 1 << 20}
}

func Client() *http.Client { return &http.Client{Transport: Transport(), Timeout: 30 * time.Second} }

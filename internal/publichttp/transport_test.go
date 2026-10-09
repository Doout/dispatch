package publichttp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestDialRejectsPrivateAndMixedDNSBeforeConnecting(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "172.20.0.1", "192.168.1.2", "169.254.169.254", "100.100.100.200", "::1", "fd00::1", "::ffff:127.0.0.1", "64:ff9b::a9fe:a9fe", "0.0.0.0", "224.0.0.1"} {
		t.Run(address, func(t *testing.T) {
			called := false
			policy := dialPolicy{resolve: func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(address)}, nil
			}, dial: func(context.Context, string, string) (net.Conn, error) {
				called = true
				return nil, errors.New("unexpected")
			}}
			if _, err := policy.DialContext(context.Background(), "tcp", "integration.example:443"); err == nil || called {
				t.Fatal("nonpublic DNS reached dialer", err)
			}
		})
	}
}
func TestDialUsesValidatedIPWithoutSecondHostnameResolution(t *testing.T) {
	resolutions := 0
	policy := dialPolicy{resolve: func(context.Context, string, string) ([]netip.Addr, error) {
		resolutions++
		return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
	}, dial: func(_ context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || address != "8.8.8.8:443" {
			t.Fatal(network, address)
		}
		return nil, errors.New("fixture dial failure")
	}}
	if _, err := policy.DialContext(context.Background(), "tcp", "integration.example:443"); err == nil || resolutions != 1 {
		t.Fatal(err, resolutions)
	}
	if Transport().Proxy != nil {
		t.Fatal("proxy environment bypasses destination policy")
	}
}

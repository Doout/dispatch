package hosted

import (
	"net"
	"net/http"
	"strings"
)

// Only configured proxies can supply client addresses. Walk from the nearest
// hop so an incoming client header cannot select its own rate-limit bucket.
func (s *Server) clientAddress(r *http.Request) *http.Request {
	trusted := func(ip net.IP) bool {
		for _, cidr := range s.Config.TrustedProxyCIDRs {
			_, network, err := net.ParseCIDR(cidr)
			if err == nil && network.Contains(ip) {
				return true
			}
		}
		return false
	}
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !trusted(net.ParseIP(peer)) {
		return r
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if len(forwarded) > 16 {
		return r
	}
	client := net.ParseIP(peer)
	for i := len(forwarded) - 1; i >= 0 && trusted(client); i-- {
		next := net.ParseIP(strings.TrimSpace(forwarded[i]))
		if next == nil {
			return r
		}
		client = next
	}
	copy := r.Clone(r.Context())
	copy.RemoteAddr = net.JoinHostPort(client.String(), "0")
	return copy
}

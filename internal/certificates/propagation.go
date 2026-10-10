package certificates

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/miekg/dns"
)

// WaitForTXT checks the authoritative nameservers directly. Each address includes
// a port, usually 53. It does not accept a recursive resolver's cached answer.
func WaitForTXT(ctx context.Context, addresses []string, challenge Challenge) error {
	if len(addresses) == 0 || challenge.Name == "" || challenge.Value == "" {
		return errors.New("DNS propagation requires nameserver addresses and a challenge")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		ready := true
		for _, address := range addresses {
			request := new(dns.Msg)
			request.SetQuestion(dns.Fqdn(challenge.Name), dns.TypeTXT)
			request.RecursionDesired = false
			request.SetEdns0(1232, false)
			response, _, err := (&dns.Client{Net: "udp", Timeout: 2 * time.Second}).ExchangeContext(ctx, request, address)
			if err == nil && response.Truncated {
				response, _, err = (&dns.Client{Net: "tcp", Timeout: 2 * time.Second}).ExchangeContext(ctx, request, address)
			}
			found := false
			if err == nil && response.Authoritative && response.Rcode == dns.RcodeSuccess {
				for _, rr := range response.Answer {
					if txt, ok := rr.(*dns.TXT); ok && strings.EqualFold(txt.Hdr.Name, request.Question[0].Name) && strings.Join(txt.Txt, "") == challenge.Value {
						found = true
						break
					}
				}
			}
			if !found {
				ready = false
			}
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

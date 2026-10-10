package certificates

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestPropagationRequiresEveryAuthoritativeNameserver(t *testing.T) {
	first, second := newFixtureSolver(t), newFixtureSolver(t)
	challenge := Challenge{Name: "_acme-challenge.team.dispatch.example.test.", Value: "value-1"}
	addresses := []string{first.address, second.address}
	if err := WaitForTXT(t.Context(), addresses, challenge); err != nil {
		t.Fatal(err)
	}
	second.mu.Lock()
	delete(second.challenges, "unrelated-1")
	second.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := WaitForTXT(ctx, addresses, challenge); err == nil {
		t.Fatal("one stale nameserver was accepted")
	}
}

func TestPropagationRejectsNonAuthoritativeOrUnrelatedAnswers(t *testing.T) {
	for _, kind := range []string{"recursive", "different name", "different value", "failed response"} {
		t.Run(kind, func(t *testing.T) {
			challenge := Challenge{Name: "_acme-challenge.team.dispatch.example.test.", Value: "expected"}
			address := serveTXTFixture(t, dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
				response := new(dns.Msg)
				response.SetReply(r)
				response.Authoritative = true
				txt := &dns.TXT{Hdr: dns.RR_Header{Name: challenge.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{challenge.Value}}
				switch kind {
				case "recursive":
					response.Authoritative = false
				case "different name":
					txt.Hdr.Name = "_acme-challenge.other.example.test."
				case "different value":
					txt.Txt = []string{"other"}
				case "failed response":
					response.Rcode = dns.RcodeServerFailure
				}
				response.Answer = []dns.RR{txt}
				_ = w.WriteMsg(response)
			}))
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			if err := WaitForTXT(ctx, []string{address}, challenge); err == nil {
				t.Fatal("unverified propagation accepted")
			}
		})
	}
}

func TestPropagationRetriesTruncatedUDPOverTCP(t *testing.T) {
	challenge := Challenge{Name: "_acme-challenge.team.dispatch.example.test.", Value: "expected-proof"}
	var tcpQueries atomic.Int32
	address := serveTXTFixture(t, dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(r)
		response.Authoritative = true
		if strings.HasPrefix(w.RemoteAddr().Network(), "udp") {
			response.Truncated = true
		} else {
			tcpQueries.Add(1)
			response.Answer = []dns.RR{&dns.TXT{Hdr: dns.RR_Header{Name: challenge.Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: []string{"expected-", "proof"}}}
		}
		_ = w.WriteMsg(response)
	}))
	if err := WaitForTXT(t.Context(), []string{address}, challenge); err != nil {
		t.Fatal(err)
	}
	if tcpQueries.Load() != 1 {
		t.Fatal("truncated answer did not retry with TCP")
	}
}

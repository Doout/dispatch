package certificates

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// The fixture holds only challenge TXT values. Public DNS and ACME providers
// are never contacted by these tests.
type fixtureSolver struct {
	mu                    sync.RWMutex
	challenges            map[string]Challenge
	address               string
	presented, cleaned    int
	failWait, failCleanup bool
}

func newFixtureSolver(t *testing.T) *fixtureSolver {
	t.Helper()
	solver := &fixtureSolver{challenges: map[string]Challenge{
		"unrelated-1": {ID: "unrelated-1", OwnerID: "another-issuer", Generation: 1, Name: "_acme-challenge.team.dispatch.example.test.", Value: "value-1"},
		"unrelated-2": {ID: "unrelated-2", OwnerID: "another-issuer", Generation: 1, Name: "_acme-challenge.team.dispatch.example.test.", Value: "value-2"},
	}}
	solver.address = serveTXTFixture(t, dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		response := new(dns.Msg)
		response.SetReply(r)
		response.Authoritative = true
		if len(r.Question) == 1 && r.Question[0].Qtype == dns.TypeTXT {
			solver.mu.RLock()
			for _, challenge := range solver.challenges {
				if strings.EqualFold(dns.Fqdn(challenge.Name), r.Question[0].Name) {
					response.Answer = append(response.Answer, &dns.TXT{Hdr: dns.RR_Header{Name: r.Question[0].Name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 60}, Txt: []string{challenge.Value}})
				}
			}
			solver.mu.RUnlock()
		}
		_ = w.WriteMsg(response)
	}))
	return solver
}

func (s *fixtureSolver) Present(_ context.Context, c Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.presented++
	if old, exists := s.challenges[c.ID]; exists && !reflect.DeepEqual(old, c) {
		return errors.New("challenge ownership mismatch")
	}
	s.challenges[c.ID] = c
	return nil
}
func (s *fixtureSolver) Wait(ctx context.Context, c Challenge) error {
	if s.failWait {
		return errors.New("nameserver not ready")
	}
	return WaitForTXT(ctx, []string{s.address}, c)
}
func (s *fixtureSolver) Cleanup(_ context.Context, c Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleaned++
	if s.failCleanup {
		return errors.New("DNS cleanup unavailable")
	}
	if old, exists := s.challenges[c.ID]; exists && !reflect.DeepEqual(old, c) {
		return errors.New("challenge ownership mismatch")
	}
	delete(s.challenges, c.ID)
	return nil
}

func serveTXTFixture(t *testing.T, handler dns.Handler) string {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		_ = tcp.Close()
		t.Fatal(err)
	}
	for _, server := range []*dns.Server{{Listener: tcp, Net: "tcp", Handler: handler}, {PacketConn: udp, Net: "udp", Handler: handler}} {
		ready := make(chan struct{})
		done := make(chan error, 1)
		server.NotifyStartedFunc = func() { close(ready) }
		go func() { done <- server.ActivateAndServe() }()
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := server.ShutdownContext(ctx); err != nil {
				t.Error(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-ctx.Done():
				t.Error("fixture DNS server did not stop")
			}
		})
		select {
		case <-ready:
		case err := <-done:
			t.Fatal("fixture DNS startup failed", err)
		case <-time.After(time.Second):
			t.Fatal("fixture DNS startup timed out")
		}
	}
	return tcp.Addr().String()
}

func query(t *testing.T, address, network, name string, kind uint16) *dns.Msg {
	t.Helper()
	request := new(dns.Msg)
	request.SetQuestion(dns.Fqdn(name), kind)
	response, _, err := (&dns.Client{Net: network, Timeout: time.Second}).Exchange(request, address)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

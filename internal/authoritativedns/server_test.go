package authoritativedns

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func fixtureSnapshot() Snapshot {
	return Snapshot{Zone: "dispatch.example.test", Generation: 1, Nameservers: []string{"ns1.example.test", "ns2.example.test"}, Records: []Record{
		{ID: "console", OwnerID: "platform", Generation: 1, Name: "team.dispatch.example.test", Type: "A", Values: []string{"192.0.2.1"}},
		{ID: "wildcard", OwnerID: "team", Generation: 1, Name: "*.team.dispatch.example.test", Type: "A", Values: []string{"192.0.2.2"}},
		{ID: "v6", OwnerID: "team", Generation: 1, Name: "*.team.dispatch.example.test", Type: "AAAA", Values: []string{"2001:db8::1"}},
		{ID: "empty-child", OwnerID: "team", Generation: 1, Name: "exists.deep.team.dispatch.example.test", Type: "A", Values: []string{"192.0.2.3"}},
		{ID: "alias", OwnerID: "team", Generation: 1, Name: "alias.team.dispatch.example.test", Type: "CNAME", Values: []string{"pr-1.team.dispatch.example.test"}},
		{ID: "challenge-1", OwnerID: "team", Generation: 1, Name: "_acme-challenge.team.dispatch.example.test", Type: "TXT", Values: []string{"value-1"}},
		{ID: "challenge-2", OwnerID: "team", Generation: 1, Name: "_acme-challenge.team.dispatch.example.test", Type: "TXT", Values: []string{"value-2"}},
		{ID: "delegation", OwnerID: "team", Generation: 1, Name: "child.team.dispatch.example.test", Type: "NS", Values: []string{"ns.child.team.dispatch.example.test"}},
		{ID: "glue", OwnerID: "team", Generation: 1, Name: "ns.child.team.dispatch.example.test", Type: "A", Values: []string{"192.0.2.4"}},
	}}
}

func serveFixture(t *testing.T, s *Server) string {
	t.Helper()
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeListeners(ctx, tcp, udp) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("DNS server did not stop")
		}
	})
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

func TestAuthoritativeUDPAndTCP(t *testing.T) {
	server, err := NewServer(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Apply(fixtureSnapshot()); err != nil {
		t.Fatal(err)
	}
	address := serveFixture(t, server)
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				kind    uint16
				rcode   int
				answers int
				aa      bool
			}{
				{"dispatch.example.test", dns.TypeSOA, dns.RcodeSuccess, 1, true},
				{"dispatch.example.test", dns.TypeNS, dns.RcodeSuccess, 2, true},
				{"team.dispatch.example.test", dns.TypeA, dns.RcodeSuccess, 1, true},
				{"pr-1.team.dispatch.example.test", dns.TypeA, dns.RcodeSuccess, 1, true},
				{"nested.pr-1.team.dispatch.example.test", dns.TypeAAAA, dns.RcodeSuccess, 1, true},
				{"alias.team.dispatch.example.test", dns.TypeA, dns.RcodeSuccess, 2, true},
				{"_acme-challenge.team.dispatch.example.test", dns.TypeTXT, dns.RcodeSuccess, 2, true},
				{"_acme-challenge.team.dispatch.example.test", dns.TypeA, dns.RcodeSuccess, 0, true},
				{"deep.team.dispatch.example.test", dns.TypeA, dns.RcodeSuccess, 0, true},
				{"missing.deep.team.dispatch.example.test", dns.TypeA, dns.RcodeNameError, 0, true},
				{"outside.example.test", dns.TypeA, dns.RcodeRefused, 0, false},
				{"dispatch.example.test", dns.TypeAXFR, dns.RcodeRefused, 0, false},
				{"dispatch.example.test", dns.TypeIXFR, dns.RcodeRefused, 0, false},
				{"child.team.dispatch.example.test", dns.TypeNS, dns.RcodeSuccess, 0, false},
			} {
				response := query(t, address, network, tc.name, tc.kind)
				if response.Rcode != tc.rcode || len(response.Answer) != tc.answers || response.Authoritative != tc.aa || response.RecursionAvailable {
					t.Errorf("%s/%d: %s", tc.name, tc.kind, response)
				}
			}
			negative := query(t, address, network, "missing.deep.team.dispatch.example.test", dns.TypeA)
			if len(negative.Ns) != 1 || negative.Ns[0].Header().Rrtype != dns.TypeSOA {
				t.Fatal("negative answer lacks SOA")
			}
			referral := query(t, address, network, "child.team.dispatch.example.test", dns.TypeNS)
			if len(referral.Ns) != 1 || len(referral.Extra) != 1 {
				t.Fatal("referral lacks NS/glue")
			}
			request := new(dns.Msg)
			request.SetUpdate("dispatch.example.test.")
			response, _, err := (&dns.Client{Net: network}).Exchange(request, address)
			if err != nil || response.Rcode != dns.RcodeRefused && response.Rcode != dns.RcodeNotImplemented {
				t.Fatalf("UPDATE accepted: %v %v", response, err)
			}
		})
	}
}

func TestTruncationAndSnapshotOwnership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zone.json")
	server, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := fixtureSnapshot()
	snapshot.Records = append(snapshot.Records, Record{ID: "large", OwnerID: "team", Generation: 1, Name: "large.team.dispatch.example.test", Type: "TXT", Values: []string{strings.Repeat("x", 2000)}})
	if err = server.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	address := serveFixture(t, server)
	if !query(t, address, "udp", "large.team.dispatch.example.test", dns.TypeTXT).Truncated {
		t.Fatal("UDP should truncate")
	}
	if response := query(t, address, "tcp", "large.team.dispatch.example.test", dns.TypeTXT); response.Truncated || len(response.Answer) != 1 {
		t.Fatal("TCP lost full answer")
	}
	snapshot.Generation = 2
	snapshot.Records[0].Values = []string{"192.0.2.9"}
	if server.Apply(snapshot) == nil {
		t.Fatal("same record generation changed")
	}
	snapshot.Records[0].Generation = 2
	if err = server.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Generation = 3
	snapshot.Records[0].OwnerID = "other"
	if server.Apply(snapshot) == nil {
		t.Fatal("ownership changed")
	}
	if server.Apply(fixtureSnapshot()) == nil {
		t.Fatal("stale zone accepted")
	}
	reopened, err := NewServer(path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.current.Load().snapshot.Generation != 2 {
		t.Fatal("durable snapshot lost")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot permissions")
	}
}

func TestAuthenticatedSnapshotReplication(t *testing.T) {
	token := strings.Repeat("s", 32)
	snapshot := fixtureSnapshot()
	source := httptest.NewTLSServer(SnapshotHandler(token, func(context.Context) (Snapshot, error) { return snapshot, nil }))
	defer source.Close()
	server, err := NewServer(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatal(err)
	}
	poller := Poller{URL: source.URL, Token: token, Server: server, Client: source.Client()}
	if err = poller.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	address := serveFixture(t, server)
	if len(query(t, address, "udp", "pr-1.team.dispatch.example.test", dns.TypeA).Answer) != 1 {
		t.Fatal("replicated route absent")
	}
	poller.Token = strings.Repeat("x", 32)
	if poller.Sync(context.Background()) == nil {
		t.Fatal("wrong credential accepted")
	}
	poller.Token = token
	snapshot.Generation = 2
	snapshot.Records = snapshot.Records[:1]
	if err = poller.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if query(t, address, "udp", "pr-1.team.dispatch.example.test", dns.TypeA).Rcode != dns.RcodeNameError {
		t.Fatal("deleted wildcard survived sync")
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, source.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	poller.URL = redirect.URL
	poller.Client = redirect.Client()
	if poller.Sync(context.Background()) == nil {
		t.Fatal("redirect accepted")
	}
	var saved Snapshot
	raw, _ := os.ReadFile(server.path)
	if json.Unmarshal(raw, &saved) != nil || saved.Generation != 2 {
		t.Fatal("failed refresh changed durable snapshot")
	}
}

func TestPropagationRequiresEveryAuthoritativeReplica(t *testing.T) {
	makeReplica := func() (*Server, string) {
		s, err := NewServer(filepath.Join(t.TempDir(), "zone.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = s.Apply(fixtureSnapshot()); err != nil {
			t.Fatal(err)
		}
		return s, serveFixture(t, s)
	}
	_, first := makeReplica()
	secondServer, second := makeReplica()
	challenge := Challenge{Name: "_acme-challenge.team.dispatch.example.test.", Value: "value-1"}
	if err := WaitForTXT(context.Background(), []string{first, second}, challenge); err != nil {
		t.Fatal(err)
	}
	snapshot := fixtureSnapshot()
	snapshot.Generation = 2
	snapshot.Records = append(snapshot.Records[:5], snapshot.Records[6:]...)
	if err := secondServer.Apply(snapshot); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if WaitForTXT(ctx, []string{first, second}, challenge) == nil {
		t.Fatal("one stale replica was accepted")
	}
}

func TestInvalidSnapshotDoesNotReplaceZone(t *testing.T) {
	server, err := NewServer(filepath.Join(t.TempDir(), "zone.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = server.Apply(fixtureSnapshot()); err != nil {
		t.Fatal(err)
	}
	for _, record := range []Record{
		{ID: "outside", OwnerID: "team", Generation: 1, Name: "elsewhere.example.test", Type: "A", Values: []string{"192.0.2.5"}},
		{ID: "apex-alias", OwnerID: "team", Generation: 1, Name: "dispatch.example.test", Type: "CNAME", Values: []string{"elsewhere.example.test"}},
		{ID: "mixed", OwnerID: "team", Generation: 1, Name: "alias.team.dispatch.example.test", Type: "TXT", Values: []string{"conflict"}},
	} {
		snapshot := fixtureSnapshot()
		snapshot.Generation = 2
		snapshot.Records = append(snapshot.Records, record)
		if server.Apply(snapshot) == nil {
			t.Fatalf("invalid record accepted: %s", record.ID)
		}
	}
	if server.current.Load().snapshot.Generation != 1 {
		t.Fatal("invalid update replaced zone")
	}
}

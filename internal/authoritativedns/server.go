package authoritativedns

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

type Server struct {
	path    string
	mu      sync.Mutex
	current atomic.Pointer[zone]
}

// NewServer loads the last accepted snapshot. A missing snapshot is allowed;
// queries receive SERVFAIL until a valid snapshot arrives.
func NewServer(snapshotPath string) (*Server, error) {
	if snapshotPath == "" || !filepath.IsAbs(snapshotPath) {
		return nil, errors.New("DNS snapshot requires an absolute path")
	}
	s := &Server{path: snapshotPath}
	raw, err := os.ReadFile(snapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot Snapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	z, err := compile(snapshot)
	if err != nil {
		return nil, err
	}
	s.current.Store(z)
	return s, nil
}

func (s *Server) Apply(snapshot Snapshot) error {
	// Own the slices before publishing an immutable snapshot to query handlers.
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	var owned Snapshot
	if err = json.Unmarshal(raw, &owned); err != nil {
		return err
	}
	next, err := compile(owned)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = validateChange(s.current.Load(), next); err != nil {
		return err
	}
	if err = atomicWrite(s.path, next.encoded); err != nil {
		return err
	}
	s.current.Store(next)
	return nil
}

func atomicWrite(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".dispatch-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Server) ServeDNS(w dns.ResponseWriter, request *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(request)
	m.RecursionAvailable = false
	m.Compress = true
	if request.Opcode != dns.OpcodeQuery {
		m.Rcode = dns.RcodeRefused
		s.write(w, request, m)
		return
	}
	if len(request.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		s.write(w, request, m)
		return
	}
	q := request.Question[0]
	if q.Qclass != dns.ClassINET || q.Qtype == dns.TypeAXFR || q.Qtype == dns.TypeIXFR || q.Qtype == dns.TypeANY {
		m.Rcode = dns.RcodeRefused
		s.write(w, request, m)
		return
	}
	z := s.current.Load()
	if z == nil {
		m.Rcode = dns.RcodeServerFailure
		s.write(w, request, m)
		return
	}
	name, err := canonical(q.Name, true)
	if err != nil {
		m.Rcode = dns.RcodeFormatError
		s.write(w, request, m)
		return
	}
	if !dns.IsSubDomain(z.snapshot.Zone, name) {
		m.Rcode = dns.RcodeRefused
		s.write(w, request, m)
		return
	}
	m.Authoritative = true
	for depth := 0; depth < 16; depth++ {
		// Stop at the first zone cut from the apex, including CNAME targets.
		if cut, ns := z.delegation(name); len(ns) > 0 {
			if q.Qtype == dns.TypeDS && cut == name {
				m.Ns = []dns.RR{dns.Copy(z.soa)}
			} else {
				m.Authoritative = len(m.Answer) > 0
				m.Ns = copyRRs(ns, "")
				for _, rr := range ns {
					host := rr.(*dns.NS).Ns
					if dns.IsSubDomain(cut, host) {
						m.Extra = append(m.Extra, copyRRs(z.nodes[host][dns.TypeA], "")...)
						m.Extra = append(m.Extra, copyRRs(z.nodes[host][dns.TypeAAAA], "")...)
					}
				}
			}
			break
		}
		sets, exists := z.nodes[name]
		if !exists {
			for ancestor := parentName(name); dns.IsSubDomain(z.snapshot.Zone, ancestor); ancestor = parentName(ancestor) {
				if _, ok := z.nodes[ancestor]; ok {
					sets, exists = z.nodes["*."+ancestor]
					break
				}
				if ancestor == z.snapshot.Zone {
					break
				}
			}
		}
		if !exists {
			m.Rcode = dns.RcodeNameError
			m.Ns = []dns.RR{dns.Copy(z.soa)}
			break
		}
		if rr := sets[q.Qtype]; len(rr) > 0 {
			m.Answer = append(m.Answer, copyRRs(rr, name)...)
			break
		}
		if aliases := sets[dns.TypeCNAME]; len(aliases) > 0 {
			m.Answer = append(m.Answer, copyRRs(aliases, name)...)
			name = aliases[0].(*dns.CNAME).Target
			if !dns.IsSubDomain(z.snapshot.Zone, name) {
				break
			}
			if depth == 15 {
				m.Rcode = dns.RcodeServerFailure
				m.Answer = nil
			}
			continue
		}
		m.Ns = []dns.RR{dns.Copy(z.soa)}
		break
	}
	s.write(w, request, m)
}

func (z *zone) delegation(name string) (string, []dns.RR) {
	var cut string
	var records []dns.RR
	for ancestor := name; ancestor != z.snapshot.Zone && ancestor != ""; ancestor = parentName(ancestor) {
		if ns := z.nodes[ancestor][dns.TypeNS]; len(ns) > 0 {
			cut, records = ancestor, ns
		}
	}
	return cut, records
}

func copyRRs(rrs []dns.RR, name string) []dns.RR {
	out := make([]dns.RR, 0, len(rrs))
	for _, rr := range rrs {
		copy := dns.Copy(rr)
		if name != "" {
			copy.Header().Name = name
		}
		out = append(out, copy)
	}
	return out
}

func (s *Server) write(w dns.ResponseWriter, request, response *dns.Msg) {
	limit := 512
	if opt := request.IsEdns0(); opt != nil {
		limit = int(opt.UDPSize())
		if limit < 512 {
			limit = 512
		}
		if limit > 1232 {
			limit = 1232
		}
		response.SetEdns0(uint16(limit), false)
		if opt.Version() != 0 {
			response.Rcode = dns.RcodeBadVers
			response.Answer, response.Ns = nil, nil
		}
	}
	if strings.HasPrefix(w.RemoteAddr().Network(), "udp") {
		response.Truncate(limit)
	}
	if response.Len() > dns.MaxMsgSize {
		response.Rcode = dns.RcodeServerFailure
		response.Answer, response.Ns, response.Extra = nil, nil, nil
	}
	_ = w.WriteMsg(response)
}

// Serve opens UDP and TCP on the same address. It never opens a recursive
// resolver, transfer listener, or unauthenticated update endpoint.
func (s *Server) Serve(ctx context.Context, address string) error {
	tcp, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", tcp.Addr().String())
	if err != nil {
		return err
	}
	defer udp.Close()
	return s.ServeListeners(ctx, tcp, udp)
}

// ServeListeners permits a caller to bind both transports before publishing
// readiness. It is also used by integration fixtures with ephemeral ports.
func (s *Server) ServeListeners(ctx context.Context, tcp net.Listener, udp net.PacketConn) error {
	servers := []*dns.Server{{Listener: tcp, Net: "tcp", Handler: s, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, MaxTCPQueries: 100}, {PacketConn: udp, Net: "udp", Handler: s, UDPSize: 1232}}
	errs := make(chan error, 2)
	var serving sync.WaitGroup
	for _, server := range servers {
		serving.Add(1)
		go func() { defer serving.Done(); errs <- server.ActivateAndServe() }()
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	for _, server := range servers {
		_ = server.Shutdown()
	}
	// Closing listeners also handles cancellation before ActivateAndServe has
	// marked a server started, when Shutdown alone cannot stop it yet.
	_ = tcp.Close()
	_ = udp.Close()
	serving.Wait()
	return err
}
